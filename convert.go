package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// convOptions é a "foto" das opções do conversor no momento em que o arquivo entrou na fila.
type convOptions struct {
	Fmt     int    // índice em convFormats
	SameDir bool   // salvar na mesma pasta do original
	OutDir  string // pasta de saída quando SameDir é falso
}

// convFormat descreve um formato/preset de saída.
type convFormat struct {
	Name   string
	Ext    string
	Args   []string // argumentos de saída do ffmpeg (vêm depois do -i)
	Tag    string   // sufixo no nome do arquivo, ex.: "whatsapp" -> "video [whatsapp].mp4"
	MaxSec float64  // 0 = sem limite; usado no GIF para ajustar a barra de progresso
	Hint   string   // explicação mostrada embaixo da lista
}

// Filtro de vídeo que garante largura e altura pares (o H.264 exige isso).
const evenScale = "scale=trunc(iw/2)*2:trunc(ih/2)*2"

var convFormats = []convFormat{
	{
		Name: "Vídeo — MP4 para WhatsApp (leve, até 720p)", Ext: "mp4", Tag: "whatsapp",
		Hint: "H.264 (perfil Main) + AAC, até 720p, otimizado para tocar no WhatsApp (celular e Web). Resolve o MP4 que \"não funciona\" no zap.",
		Args: []string{
			"-map", "0:v:0", "-map", "0:a:0?",
			"-c:v", "libx264", "-preset", "medium", "-crf", "27", "-profile:v", "main", "-level", "4.0", "-pix_fmt", "yuv420p",
			"-vf", "scale='if(gt(iw,ih),min(1280,iw),min(720,iw))':-2," + evenScale,
			"-c:a", "aac", "-b:a", "96k", "-ar", "44100", "-ac", "2",
			"-movflags", "+faststart",
		},
	},
	{
		Name: "Vídeo — MP4 compatível (H.264 + AAC)", Ext: "mp4",
		Hint: "Mantém a resolução original, recodifica para H.264 + AAC. Toca em praticamente qualquer aparelho.",
		Args: []string{
			"-map", "0:v:0", "-map", "0:a:0?",
			"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p",
			"-vf", evenScale,
			"-c:a", "aac", "-b:a", "192k",
			"-movflags", "+faststart",
		},
	},
	{
		Name: "Vídeo — MKV (só troca a caixa, sem perder qualidade)", Ext: "mkv",
		Hint: "Não recodifica: copia o vídeo e o áudio para dentro de um MKV. É rápido e não perde qualidade.",
		Args: []string{"-map", "0:v?", "-map", "0:a?", "-c", "copy"},
	},
	{
		Name: "Vídeo — MOV (H.264 + AAC)", Ext: "mov",
		Hint: "H.264 + AAC dentro de um MOV (bom para editores de vídeo e aparelhos Apple).",
		Args: []string{
			"-map", "0:v:0", "-map", "0:a:0?",
			"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p",
			"-vf", evenScale,
			"-c:a", "aac", "-b:a", "192k",
		},
	},
	{
		Name: "Vídeo — WebM (VP9 + Opus)", Ext: "webm",
		Hint: "Formato aberto para web. A conversão é mais lenta que as outras.",
		Args: []string{
			"-map", "0:v:0", "-map", "0:a:0?",
			"-c:v", "libvpx-vp9", "-crf", "32", "-b:v", "0", "-row-mt", "1", "-deadline", "good", "-cpu-used", "4", "-pix_fmt", "yuv420p",
			"-c:a", "libopus", "-b:a", "128k",
		},
	},
	{
		Name: "Vídeo — AVI (MPEG-4 + MP3)", Ext: "avi",
		Hint: "Formato antigo, para aparelhos e programas que só aceitam AVI.",
		Args: []string{
			"-map", "0:v:0", "-map", "0:a:0?",
			"-c:v", "mpeg4", "-q:v", "4", "-pix_fmt", "yuv420p",
			"-c:a", "libmp3lame", "-q:a", "4",
		},
	},
	{
		Name: "Vídeo — GIF animado (480 px, até 20 s)", Ext: "gif", MaxSec: 20,
		Hint: "Pega os primeiros 20 segundos, 480 px de largura, sem áudio. GIF de vídeo longo fica gigante.",
		Args: []string{
			"-t", "20", "-an",
			"-vf", "fps=12,scale=480:-1:flags=lanczos,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse",
			"-loop", "0",
		},
	},
	{
		Name: "Áudio — OPUS voz (WhatsApp, bem leve)", Ext: "opus", Tag: "voz",
		Hint: "OPUS mono de 32 kbps, o mesmo tipo das mensagens de voz do WhatsApp. Ótimo para fala, fraco para música.",
		Args: []string{"-map", "0:a:0", "-vn", "-c:a", "libopus", "-b:a", "32k", "-ac", "1", "-ar", "48000", "-application", "voip"},
	},
	{
		Name: "Áudio — OPUS música (alta qualidade)", Ext: "opus",
		Hint: "OPUS de 128 kbps: som muito bom em pouco espaço.",
		Args: []string{"-map", "0:a:0", "-vn", "-map_metadata", "0", "-c:a", "libopus", "-b:a", "128k", "-application", "audio"},
	},
	{
		Name: "Áudio — MP3", Ext: "mp3",
		Hint: "MP3 de qualidade alta (VBR ~190 kbps). Toca em qualquer lugar.",
		Args: []string{"-map", "0:a:0", "-vn", "-map_metadata", "0", "-c:a", "libmp3lame", "-q:a", "2"},
	},
	{
		Name: "Áudio — M4A (AAC)", Ext: "m4a",
		Hint: "AAC de 192 kbps, o formato padrão de celulares e iTunes.",
		Args: []string{"-map", "0:a:0", "-vn", "-map_metadata", "0", "-c:a", "aac", "-b:a", "192k"},
	},
	{
		Name: "Áudio — WAV (sem compressão)", Ext: "wav",
		Hint: "Som sem compressão. Arquivo grande, qualidade total.",
		Args: []string{"-map", "0:a:0", "-vn", "-c:a", "pcm_s16le"},
	},
	{
		Name: "Áudio — FLAC (sem perdas)", Ext: "flac",
		Hint: "Compressão sem perda de qualidade, bem menor que o WAV.",
		Args: []string{"-map", "0:a:0", "-vn", "-map_metadata", "0", "-c:a", "flac"},
	},
	{
		Name: "Áudio — OGG (Vorbis)", Ext: "ogg",
		Hint: "Vorbis de qualidade alta, formato aberto.",
		Args: []string{"-map", "0:a:0", "-vn", "-map_metadata", "0", "-c:a", "libvorbis", "-q:a", "5"},
	},
}

func convNames() []string {
	out := make([]string, len(convFormats))
	for i, f := range convFormats {
		out[i] = f.Name
	}
	return out
}

// Extensões aceitas quando uma PASTA é arrastada para o conversor.
var mediaExts = map[string]bool{
	".mp4": true, ".mkv": true, ".mov": true, ".avi": true, ".webm": true, ".flv": true,
	".wmv": true, ".m4v": true, ".3gp": true, ".ts": true, ".mpg": true, ".mpeg": true,
	".mp3": true, ".m4a": true, ".wav": true, ".flac": true, ".ogg": true, ".opus": true,
	".aac": true, ".wma": true,
}

// expandPaths transforma uma lista de caminhos em arquivos: arquivos soltos
// entram como estão; pastas entram com todos os vídeos/áudios que tiverem dentro.
func expandPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			out = append(out, p)
			continue
		}
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if mediaExts[strings.ToLower(filepath.Ext(path))] {
				out = append(out, path)
			}
			return nil
		})
	}
	return out
}

// uniqueOut escolhe um nome de saída que ainda não existe (o original nunca é sobrescrito).
func uniqueOut(dir, src string, f convFormat) string {
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	if f.Tag != "" {
		base += " [" + f.Tag + "]"
	}
	cand := filepath.Join(dir, base+"."+f.Ext)
	for i := 1; i < 1000; i++ {
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
		cand = filepath.Join(dir, fmt.Sprintf("%s (%d).%s", base, i, f.Ext))
	}
	return cand
}

// addConvFiles coloca arquivos na fila de conversão e já começa. Roda na thread da janela.
// Devolve quantos entraram.
func (a *SnowApp) addConvFiles(paths []string, opts convOptions) int {
	files := expandPaths(paths)

	// Ignora o que já está esperando ou convertendo com o mesmo caminho.
	busy := map[string]bool{}
	a.ctable.mu.Lock()
	for _, it := range a.ctable.items {
		if it.State == stWaiting || it.State == stRunning {
			busy[strings.ToLower(it.URL)] = true
		}
	}
	from := len(a.ctable.items)
	added := 0
	for _, p := range files {
		k := strings.ToLower(p)
		if busy[k] {
			continue
		}
		busy[k] = true
		a.cnextID++
		a.ctable.items = append(a.ctable.items, &DlJob{
			ID:     a.cnextID,
			URL:    p,
			Title:  filepath.Base(p),
			Status: "Na fila",
			Conv:   opts,
		})
		added++
	}
	a.ctable.mu.Unlock()

	if added > 0 {
		a.ctable.PublishRowsInserted(from, from+added-1)
		a.startConvQueue()
	}
	return added
}

// startConvQueue acorda o conversor (um arquivo por vez: o ffmpeg já usa todos os núcleos).
func (a *SnowApp) startConvQueue() {
	a.cqmu.Lock()
	if a.cqRunning {
		a.cqmu.Unlock()
		return
	}
	a.cqRunning = true
	a.cqmu.Unlock()

	go func() {
		for {
			// Espera o ffmpeg estar pronto (primeira abertura baixa ele).
			if !fileExists(a.tools.Ffmpeg) || !fileExists(a.tools.Ffprobe) {
				a.setStatus("Aguardando o ffmpeg ser baixado…")
				time.Sleep(500 * time.Millisecond)
				continue
			}

			var next *DlJob
			a.ctable.mu.Lock()
			for _, it := range a.ctable.items {
				if it.State == stWaiting && !it.canceled {
					it.State = stRunning
					it.Status = "Iniciando…"
					next = it
					break
				}
			}
			a.ctable.mu.Unlock()

			if next == nil {
				// Reconfere com o trinco da fila para não perder um item que acabou de entrar.
				a.cqmu.Lock()
				pending := false
				a.ctable.mu.Lock()
				for _, it := range a.ctable.items {
					if it.State == stWaiting && !it.canceled {
						pending = true
						break
					}
				}
				a.ctable.mu.Unlock()
				if !pending {
					a.cqRunning = false
					a.cqmu.Unlock()
					return
				}
				a.cqmu.Unlock()
				continue
			}

			a.refreshIn(a.ctable, next)
			a.runConv(next)
		}
	}()
}

// probeDuration pergunta ao ffprobe a duração do arquivo, em segundos (0 = não sei).
func (a *SnowApp) probeDuration(path string) float64 {
	cmd := exec.Command(a.tools.Ffprobe, "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// fmtETA transforma segundos em "1:05" ou "1:02:03".
func fmtETA(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec + 0.5)
	h, m, ss := s/3600, (s%3600)/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, ss)
	}
	return fmt.Sprintf("%d:%02d", m, ss)
}

// runConv converte um arquivo do começo ao fim.
func (a *SnowApp) runConv(j *DlJob) {
	t := a.ctable
	t.mu.Lock()
	src, copts := j.URL, j.Conv
	t.mu.Unlock()

	if !fileExists(src) {
		a.finishConv(j, stError, "Erro: o arquivo de origem não foi encontrado")
		return
	}

	f := convFormats[clamp(copts.Fmt, len(convFormats))]
	outDir := filepath.Dir(src)
	if !copts.SameDir && strings.TrimSpace(copts.OutDir) != "" {
		outDir = copts.OutDir
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		a.finishConv(j, stError, "Erro: não consegui criar a pasta de saída")
		return
	}
	out := uniqueOut(outDir, src, f)

	dur := a.probeDuration(src)
	if f.MaxSec > 0 && (dur == 0 || dur > f.MaxSec) {
		dur = f.MaxSec
	}

	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-i", src}
	args = append(args, f.Args...)
	args = append(args, "-progress", "pipe:1", "-nostats", out)

	cmd := exec.Command(a.tools.Ffmpeg, args...)
	hideWindow(cmd)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.finishConv(j, stError, "Erro interno: "+err.Error())
		return
	}

	t.mu.Lock()
	if j.canceled {
		t.mu.Unlock()
		a.finishConv(j, stCanceled, "Cancelado")
		return
	}
	j.cmd = cmd
	t.mu.Unlock()

	if err := cmd.Start(); err != nil {
		a.finishConv(j, stError, "Erro ao iniciar o ffmpeg: "+err.Error())
		return
	}

	var cur float64
	speedStr := ""
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_us", "out_time_ms": // as duas vêm em microssegundos
			if us, err := strconv.ParseFloat(v, 64); err == nil && us >= 0 {
				cur = us / 1e6
			}
		case "speed":
			speedStr = strings.TrimSpace(v)
		case "progress":
			// fim de um bloco de informações: atualiza a tela
			pct, pctVal, eta := "—", -1.0, "—"
			if dur > 0 {
				pctVal = cur / dur * 100
				if pctVal > 99.9 {
					pctVal = 99.9
				}
				pct = fmt.Sprintf("%.1f%%", pctVal)
				if sp, err := strconv.ParseFloat(strings.TrimSuffix(speedStr, "x"), 64); err == nil && sp > 0 {
					eta = fmtETA((dur - cur) / sp)
				}
			}
			spd := speedStr
			if spd == "" || strings.EqualFold(spd, "N/A") {
				spd = "—"
			}
			t.mu.Lock()
			j.Percent, j.PctVal, j.Speed, j.ETA = pct, pctVal, spd, eta
			j.Status = "Convertendo"
			t.mu.Unlock()
			a.refreshIn(t, j)
		}
	}
	waitErr := cmd.Wait()

	t.mu.Lock()
	wasCanceled := j.canceled
	j.cmd = nil
	t.mu.Unlock()

	switch {
	case wasCanceled:
		os.Remove(out)
		a.finishConv(j, stCanceled, "Cancelado")
	case waitErr == nil:
		t.mu.Lock()
		j.FilePath = out
		t.mu.Unlock()
		a.finishConv(j, stDone, "Concluído ✔")
	default:
		os.Remove(out) // não deixa arquivo pela metade
		a.finishConv(j, stError, "Erro: "+friendlyConvError(errBuf.String()))
	}
}

// friendlyConvError traduz os erros mais comuns do ffmpeg.
func friendlyConvError(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "matches no streams"), strings.Contains(low, "does not contain any stream"),
		strings.Contains(low, "option 'map'"):
		return "esse arquivo não tem áudio/vídeo para esse formato"
	case strings.Contains(low, "invalid data found"):
		return "arquivo corrompido ou não é um vídeo/áudio"
	case strings.Contains(low, "unknown encoder"), strings.Contains(low, "unrecognized option"):
		return "o ffmpeg desta máquina não tem esse codec (apague a pasta tools para baixar de novo)"
	case strings.Contains(low, "permission denied"):
		return "sem permissão para gravar nessa pasta"
	case strings.Contains(low, "no space left"):
		return "sem espaço no disco"
	}
	// Pega a última linha não vazia.
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return "falha desconhecida"
	}
	return shorten(last, 110)
}

func (a *SnowApp) finishConv(j *DlJob, st jobState, status string) {
	t := a.ctable
	t.mu.Lock()
	j.State = st
	j.Status = status
	j.Speed, j.ETA = "", ""
	if st == stDone {
		j.PctVal = 100
		j.Percent = "100%"
	}
	name := j.Title
	t.mu.Unlock()
	a.refreshIn(t, j)

	switch st {
	case stDone:
		a.notify("Conversão concluída", shorten(name, 90), false)
	case stError:
		a.notify("Falha na conversão", shorten(name, 70)+"\n"+status, true)
	}
}

// cancelConv cancela um item da lista de conversão (esperando ou em andamento).
func (a *SnowApp) cancelConv(j *DlJob) {
	t := a.ctable
	t.mu.Lock()
	if j.State == stDone || j.State == stError || j.State == stCanceled {
		t.mu.Unlock()
		return
	}
	j.canceled = true
	cmd := j.cmd
	wasWaiting := j.State == stWaiting
	t.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		killTree(cmd.Process.Pid)
	}
	if wasWaiting {
		a.finishConv(j, stCanceled, "Cancelado")
	}
}
