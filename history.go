package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// histEntry é o registro de um vídeo já baixado.
type histEntry struct {
	Title string `json:"title"`
	File  string `json:"file"`
	When  int64  `json:"when"` // unix
}

// History lembra o que já foi baixado, para avisar quando o mesmo link
// for colado de novo.
type History struct {
	mu sync.Mutex
	m  map[string]histEntry
}

func historyPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "SnowDownloader", "history.json")
}

func loadHistory() *History {
	h := &History{m: map[string]histEntry{}}
	b, err := os.ReadFile(historyPath())
	if err != nil {
		return h
	}
	_ = json.Unmarshal(b, &h.m)
	if h.m == nil {
		h.m = map[string]histEntry{}
	}
	return h
}

// save grava o histórico em disco. Quem chama já está com o lock.
func (h *History) save() {
	p := historyPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, err := json.MarshalIndent(h.m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o644)
}

func (h *History) Get(link string) (histEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.m[normURL(link)]
	return e, ok
}

func (h *History) Add(link, title, file string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.m[normURL(link)] = histEntry{Title: title, File: file, When: time.Now().Unix()}
	h.save()
}

func (h *History) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.m)
}

func (h *History) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.m = map[string]histEntry{}
	h.save()
}

// normURL deixa links "iguais" com a mesma forma (youtu.be/X e
// youtube.com/watch?v=X contam como o mesmo vídeo; # e / no fim não importam).
func normURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")

	switch host {
	case "youtu.be":
		if id := strings.Trim(u.Path, "/"); id != "" {
			return "https://youtube.com/watch?v=" + id
		}
	case "youtube.com":
		if u.Path == "/watch" {
			if v := u.Query().Get("v"); v != "" {
				return "https://youtube.com/watch?v=" + v
			}
		}
		if strings.HasPrefix(u.Path, "/shorts/") {
			if id := strings.Trim(strings.TrimPrefix(u.Path, "/shorts/"), "/"); id != "" {
				return "https://youtube.com/watch?v=" + id
			}
		}
	}

	u.Host = host
	u.Scheme = strings.ToLower(u.Scheme)
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}
