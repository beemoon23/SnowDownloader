package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AppSettings é o que o app lembra entre uma abertura e outra.
type AppSettings struct {
	OutDir      string `json:"outDir"`
	Quality     int    `json:"quality"`
	Cookies     int    `json:"cookies"`
	Concurrency int    `json:"concurrency"`
	Thumbnail   bool   `json:"thumbnail"`
	Subtitles   bool   `json:"subtitles"`
	Playlist    bool   `json:"playlist"`
	LiveFromBeg bool   `json:"liveFromStart"`
	ExtraArgs   string `json:"extraArgs"`

	TrayMin        bool  `json:"trayMin"`        // minimizar para a bandeja
	LastYtDlpCheck int64 `json:"lastYtDlpCheck"` // unix da última atualização automática do yt-dlp
}

func defaultSettings() AppSettings {
	home, _ := os.UserHomeDir()
	return AppSettings{
		OutDir:      filepath.Join(home, "Videos", "SnowDownloader"),
		Quality:     0,
		Cookies:     0,
		Concurrency: 1, // índice do combo => 2 downloads simultâneos
		Thumbnail:   true,
		TrayMin:     true,
	}
}

func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "SnowDownloader", "settings.json")
}

func loadSettings() AppSettings {
	s := defaultSettings()
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	if s.OutDir == "" {
		s.OutDir = defaultSettings().OutDir
	}
	return s
}

func saveSettings(s AppSettings) {
	p := settingsPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o644)
}
