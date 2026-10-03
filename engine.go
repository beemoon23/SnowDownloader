package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Opções de qualidade mostradas no combo. A ordem aqui é a ordem na tela.
type qualityOption struct {
	Name   string
	Format string // -f do yt-dlp ("" quando for só áudio)
	Audio  string // formato de áudio (mp3, m4a, best...) quando for só áudio
}

var qualityOptions = []qualityOption{
	{Name: "Melhor qualidade disponível (vídeo + áudio)", Format: "bv*+ba/b"},
	{Name: "Até 2160p (4K)", Format: "bv*[height<=2160]+ba/b[height<=2160]"},
	{Name: "Até 1440p", Format: "bv*[height<=1440]+ba/b[height<=1440]"},
	{Name: "Até 1080p (Full HD)", Format: "bv*[height<=1080]+ba/b[height<=1080]"},
	{Name: "Até 720p", Format: "bv*[height<=720]+ba/b[height<=720]"},
	{Name: "Até 480p", Format: "bv*[height<=480]+ba/b[height<=480]"},
	{Name: "Só áudio — MP3", Audio: "mp3"},
	{Name: "Só áudio — M4A", Audio: "m4a"},
	{Name: "Só áudio — melhor, formato original", Audio: "best"},
}

var cookieBrowsers = []string{"Nenhum", "chrome", "edge", "firefox", "brave", "opera", "vivaldi"}

func qualityNames() []string {
	out := make([]string, len(qualityOptions))
	for i, q := range qualityOptions {
		out[i] = q.Name
	}
	return out
}

func cookieNames() []string {
	out := make([]string, len(cookieBrowsers))
	for i, c := range cookieBrowsers {
		if c == "Nenhum" {
			out[i] = c
		} else {
			out[i] = strings.ToUpper(c[:1]) + c[1:]
		}
	}
	return out
}

const progressTemplate = "download:SNOW|%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s|%(info.title)s"

// buildArgs monta a linha de comando do yt-dlp para um job.
func (a *SnowApp) buildArgs(j *DlJob) []string {
	o := j.Opts
	args := []string{
		"--newline",
		"--no-colors",
		"--encoding", "utf-8",
		"--ffmpeg-location", a.tools.Dir,
		"--progress-template", progressTemplate,
		"-P", o.OutDir,
		"-o", "%(title).150B [%(id)s].%(ext)s",
		"--windows-filenames",
		"--no-mtime",
		"--retries", "10",
		"--fragment-retries", "10",
		// Se uma live for interrompida, o arquivo continua tocável.
		"--hls-use-mpegts",
	}

	q := qualityOptions[0]
	if o.Quality >= 0 && o.Quality < len(qualityOptions) {
		q = qualityOptions[o.Quality]
	}
	if q.Audio != "" {
		args = append(args, "-x", "--audio-format", q.Audio, "--audio-quality", "0")
	} else {
		// -S ordena por: maior resolução, depois prefere vídeo H.264 e áudio
		// AAC (m4a). Sem isso o YouTube entrega áudio Opus, que dentro de um
		// .mp4 fica mudo na maioria dos players do Windows.
		args = append(args,
			"-f", q.Format,
			"-S", "res,vcodec:h264,acodec:m4a",
			"--merge-output-format", "mp4",
		)
	}

	if o.Playlist {
		args = append(args, "--yes-playlist")
	} else {
		args = append(args, "--no-playlist")
	}
	if o.Thumbnail {
		args = append(args, "--embed-thumbnail", "--embed-metadata")
	}
	if o.Subtitles && q.Audio == "" {
		args = append(args, "--write-subs", "--sub-langs", "pt.*,en.*", "--embed-subs")
	}
	if o.LiveFromBeg {
		args = append(args, "--live-from-start")
	}
	if o.Cookies > 0 && o.Cookies < len(cookieBrowsers) {
		args = append(args, "--cookies-from-browser", cookieBrowsers[o.Cookies])
	}
	if extra := strings.Fields(o.ExtraArgs); len(extra) > 0 {
		args = append(args, extra...)
	}

	args = append(args, "--", j.URL)
	return args
}

// startQueue acorda o agendador, se ele ainda não estiver rodando.
func (a *SnowApp) startQueue() {
	a.qmu.Lock()
	if a.queueRunning {
		a.qmu.Unlock()
		return
	}
	a.queueRunning = true
	a.qmu.Unlock()

	go func() {
		for {
			// Espera as ferramentas (yt-dlp/ffmpeg) estarem prontas.
			if !a.tools.Ready() {
				time.Sleep(500 * time.Millisecond)
				continue
			}

			a.qmu.Lock()
			max := a.maxConcurrent
			if max < 1 {
				max = 1
			}
			var next *DlJob
			if a.active < max {
				a.table.mu.Lock()
				for _, it := range a.table.items {
					if it.State == stWaiting && !it.canceled {
						it.State = stRunning
						it.Status = "Iniciando…"
						next = it
						break
					}
				}
				a.table.mu.Unlock()
			}

			// Há algo esperando, mas não há vaga? Aguarda. Nada esperando? Encerra.
			if next == nil {
				waiting := false
				a.table.mu.Lock()
				for _, it := range a.table.items {
					if it.State == stWaiting && !it.canceled {
						waiting = true
						break
					}
				}
				a.table.mu.Unlock()
				if !waiting {
					a.queueRunning = false
					a.qmu.Unlock()
					return
				}
				a.qmu.Unlock()
				time.Sleep(300 * time.Millisecond)
				continue
			}

			a.active++
			a.qmu.Unlock()

			a.refresh(next)
			go func(j *DlJob) {
				a.runJob(j)
				a.qmu.Lock()
				a.active--
				a.qmu.Unlock()
			}(next)
		}
	}()
}

// pathFromLine tenta achar o caminho do arquivo numa linha de saída do yt-dlp.
// Devolve "" quando a linha não fala de um arquivo de vídeo/áudio final.
func pathFromLine(line string) string {
	var p string
	switch {
	case strings.HasPrefix(line, "[Merger] Merging formats into "):
		p = strings.Trim(strings.TrimPrefix(line, "[Merger] Merging formats into "), "\"")
	case strings.HasPrefix(line, "[ExtractAudio] Destination: "):
		p = strings.TrimPrefix(line, "[ExtractAudio] Destination: ")
	case strings.HasPrefix(line, "[VideoRemuxer] ") && strings.Contains(line, "Destination: "):
		p = line[strings.Index(line, "Destination: ")+len("Destination: "):]
	case strings.HasPrefix(line, "[download] Destination: "):
		p = strings.TrimPrefix(line, "[download] Destination: ")
	case strings.HasPrefix(line, "[download] ") && strings.HasSuffix(line, " has already been downloaded"):
		p = strings.TrimSuffix(strings.TrimPrefix(line, "[download] "), " has already been downloaded")
	}
	p = strings.TrimSpace(p)

	// Ignora legendas, miniaturas e arquivos temporários.
	switch strings.ToLower(filepath.Ext(p)) {
	case "", ".vtt", ".srt", ".ass", ".lrc", ".json", ".jpg", ".jpeg", ".png", ".webp", ".part", ".ytdl":
		return ""
	}
	return p
}

// shorten corta um texto longo sem quebrar caracteres acentuados.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func cleanVal(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	if s == "" || low == "n/a" || strings.HasPrefix(low, "unknown") || low == "none" {
		return "—"
	}
	return s
}

// friendlyError traduz os erros mais comuns do yt-dlp.
func friendlyError(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "sign in to confirm"), strings.Contains(low, "not a bot"):
		return "YouTube pediu login — escolha seu navegador em \"Cookies\""
	case strings.Contains(low, "login required"), strings.Contains(low, "rate-limit"),
		strings.Contains(low, "requires authentication"), strings.Contains(low, "private"):
		return "Precisa de login — escolha seu navegador em \"Cookies\""
	case strings.Contains(low, "requested format is not available"):
		return "Qualidade indisponível — tente \"Melhor qualidade\""
	case strings.Contains(low, "unsupported url"):
		return "Link não suportado"
	case strings.Contains(low, "video unavailable"), strings.Contains(low, "has been removed"):
		return "Vídeo indisponível"
	case strings.Contains(low, "drm"):
		return "Conteúdo com DRM — não dá para baixar"
	case strings.Contains(low, "could not copy") && strings.Contains(low, "cookie"):
		return "Não consegui ler os cookies — feche o navegador e tente de novo"
	}
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "ERROR:"))
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	return msg
}

// runJob executa um download do começo ao fim.
func (a *SnowApp) runJob(j *DlJob) {
	if err := os.MkdirAll(j.Opts.OutDir, 0o755); err != nil {
		a.finish(j, stError, "Erro: não consegui criar a pasta de destino")
		return
	}

	cmd := exec.Command(a.tools.YtDlp, a.buildArgs(j)...)
	hideWindow(cmd)

	pr, pw, err := os.Pipe()
	if err != nil {
		a.finish(j, stError, "Erro interno: "+err.Error())
		return
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	a.table.mu.Lock()
	if j.canceled {
		a.table.mu.Unlock()
		pr.Close()
		pw.Close()
		a.finish(j, stCanceled, "Cancelado")
		return
	}
	j.cmd = cmd
	a.table.mu.Unlock()

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		a.finish(j, stError, "Erro ao iniciar yt-dlp: "+err.Error())
		return
	}
	pw.Close() // só o processo filho mantém o lado de escrita aberto

	lastErr := ""
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "SNOW|"):
			parts := strings.SplitN(line, "|", 5)
			if len(parts) < 5 {
				continue
			}
			pct := cleanVal(parts[1])
			pctVal := -1.0
			if v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(parts[1]), "%"), 64); err == nil {
				pctVal = v
			}
			a.table.mu.Lock()
			j.Percent = pct
			j.PctVal = pctVal
			j.Speed = cleanVal(parts[2])
			j.ETA = cleanVal(parts[3])
			if t := strings.TrimSpace(parts[4]); t != "" && !strings.EqualFold(t, "NA") {
				j.Title = t
			}
			if pct == "—" {
				j.Status = "Baixando / gravando…" // live: não existe porcentagem
			} else {
				j.Status = "Baixando"
			}
			a.table.mu.Unlock()
			a.refresh(j)
		case strings.HasPrefix(line, "[Merger]"), strings.HasPrefix(line, "[VideoRemuxer]"):
			a.setJob(j, "Juntando áudio e vídeo…")
		case strings.HasPrefix(line, "[ExtractAudio]"):
			a.setJob(j, "Convertendo áudio…")
		case strings.HasPrefix(line, "[EmbedThumbnail]"), strings.HasPrefix(line, "[Metadata]"),
			strings.HasPrefix(line, "[EmbedSubtitle]"):
			a.setJob(j, "Finalizando arquivo…")
		case strings.HasPrefix(line, "ERROR:"):
			lastErr = line
		}

		// Guarda o caminho do arquivo (o último é o final) para poder abrir depois.
		if p := pathFromLine(line); p != "" {
			a.table.mu.Lock()
			j.FilePath = p
			a.table.mu.Unlock()
		}
	}
	pr.Close()
	waitErr := cmd.Wait()

	a.table.mu.Lock()
	wasCanceled := j.canceled
	j.cmd = nil
	a.table.mu.Unlock()

	switch {
	case wasCanceled:
		a.finish(j, stCanceled, "Cancelado")
	case waitErr == nil:
		a.table.mu.Lock()
		j.Percent = "100%"
		j.Speed = ""
		j.ETA = ""
		a.table.mu.Unlock()
		a.finish(j, stDone, "Concluído ✔")
	default:
		msg := "Erro"
		if lastErr != "" {
			msg = "Erro: " + friendlyError(lastErr)
		}
		a.finish(j, stError, msg)
	}
}

func (a *SnowApp) setJob(j *DlJob, status string) {
	a.table.mu.Lock()
	j.Status = status
	a.table.mu.Unlock()
	a.refresh(j)
}

func (a *SnowApp) finish(j *DlJob, st jobState, status string) {
	a.table.mu.Lock()
	j.State = st
	j.Status = status
	if st != stDone {
		j.Speed = ""
		j.ETA = ""
	}
	if st == stDone {
		j.PctVal = 100
	}
	name := j.Title
	if name == "" {
		name = j.URL
	}
	a.table.mu.Unlock()
	a.refresh(j)

	// Aviso do Windows (balão na bandeja) quando termina ou falha.
	switch st {
	case stDone:
		a.notify("Download concluído", shorten(name, 90), false)
	case stError:
		a.notify("Falha no download", shorten(name, 70)+"\n"+status, true)
	}
}

// cancelJob cancela um item (esperando ou em andamento).
func (a *SnowApp) cancelJob(j *DlJob) {
	a.table.mu.Lock()
	if j.State == stDone || j.State == stError || j.State == stCanceled {
		a.table.mu.Unlock()
		return
	}
	j.canceled = true
	cmd := j.cmd
	wasWaiting := j.State == stWaiting
	a.table.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		killTree(cmd.Process.Pid)
	}
	if wasWaiting {
		a.finish(j, stCanceled, "Cancelado")
	}
}
