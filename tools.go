package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
)

const (
	ytDlpURL  = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"
	ffmpegURL = "https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip"
)

// ToolPaths guarda onde ficam yt-dlp e ffmpeg.
type ToolPaths struct {
	Dir     string
	YtDlp   string
	Ffmpeg  string
	Ffprobe string
}

// pickToolsDir usa a pasta "tools" ao lado do .exe (modo portátil).
// Se não der para escrever lá (ex.: Program Files), usa %LOCALAPPDATA%.
func pickToolsDir() string {
	if exe, err := os.Executable(); err == nil {
		d := filepath.Join(filepath.Dir(exe), "tools")
		if os.MkdirAll(d, 0o755) == nil {
			probe := filepath.Join(d, ".write-test")
			if f, err := os.Create(probe); err == nil {
				f.Close()
				os.Remove(probe)
				return d
			}
		}
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	d := filepath.Join(base, "SnowDownloader", "tools")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func newToolPaths() ToolPaths {
	d := pickToolsDir()
	return ToolPaths{
		Dir:     d,
		YtDlp:   filepath.Join(d, "yt-dlp.exe"),
		Ffmpeg:  filepath.Join(d, "ffmpeg.exe"),
		Ffprobe: filepath.Join(d, "ffprobe.exe"),
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func (t ToolPaths) Ready() bool {
	return fileExists(t.YtDlp) && fileExists(t.Ffmpeg) && fileExists(t.Ffprobe)
}

// downloadFile baixa uma URL para dest, avisando o progresso.
func downloadFile(url, dest string, onProgress func(done, total int64)) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("servidor respondeu %s", resp.Status)
	}

	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}

	var done int64
	buf := make([]byte, 128*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
			done += int64(n)
			if onProgress != nil {
				onProgress(done, resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Remove(dest)
	return os.Rename(tmp, dest)
}

// extractFFmpeg tira ffmpeg.exe e ffprobe.exe de dentro do zip.
func extractFFmpeg(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	want := map[string]bool{"ffmpeg.exe": true, "ffprobe.exe": true}
	found := 0
	for _, f := range zr.File {
		name := strings.ToLower(filepath.Base(f.Name))
		if !want[name] || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(destDir, name))
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if cerr != nil {
			return cerr
		}
		found++
	}
	if found < 2 {
		return fmt.Errorf("ffmpeg.exe/ffprobe.exe não encontrados dentro do zip")
	}
	return nil
}

func human(done, total int64) string {
	mb := func(b int64) float64 { return float64(b) / 1024 / 1024 }
	if total > 0 {
		return fmt.Sprintf("%.0f / %.0f MB", mb(done), mb(total))
	}
	return fmt.Sprintf("%.0f MB", mb(done))
}

// ensureTools baixa yt-dlp e ffmpeg na primeira execução.
func (a *SnowApp) ensureTools() error {
	t := a.tools
	if err := os.MkdirAll(t.Dir, 0o755); err != nil {
		return err
	}

	if !fileExists(t.YtDlp) {
		a.setStatus("Baixando yt-dlp (só na primeira vez)…")
		err := downloadFile(ytDlpURL, t.YtDlp, func(d, tot int64) {
			a.setStatus("Baixando yt-dlp… " + human(d, tot))
		})
		if err != nil {
			return fmt.Errorf("falha ao baixar yt-dlp: %w", err)
		}
	}

	if !fileExists(t.Ffmpeg) || !fileExists(t.Ffprobe) {
		zipPath := filepath.Join(t.Dir, "ffmpeg.zip")
		a.setStatus("Baixando ffmpeg (só na primeira vez, ~150 MB)…")
		err := downloadFile(ffmpegURL, zipPath, func(d, tot int64) {
			a.setStatus("Baixando ffmpeg… " + human(d, tot))
		})
		if err != nil {
			return fmt.Errorf("falha ao baixar ffmpeg: %w", err)
		}
		a.setStatus("Extraindo ffmpeg…")
		if err := extractFFmpeg(zipPath, t.Dir); err != nil {
			return fmt.Errorf("falha ao extrair ffmpeg: %w", err)
		}
		os.Remove(zipPath)
	}
	return nil
}

func (a *SnowApp) ytDlpVersion() string {
	cmd := exec.Command(a.tools.YtDlp, "--version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(out))
}

// updateYtDlp roda "yt-dlp -U" (o próprio yt-dlp se atualiza).
// Enquanto atualiza, a fila de downloads espera (ytBusy), porque o Windows
// não deixa trocar o yt-dlp.exe com ele em uso.
func (a *SnowApp) updateYtDlp() (string, error) {
	atomic.StoreInt32(&a.ytBusy, 1)
	defer atomic.StoreInt32(&a.ytBusy, 0)

	cmd := exec.Command(a.tools.YtDlp, "-U")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
