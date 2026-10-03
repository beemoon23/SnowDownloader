package main

import (
	"os/exec"
	"sync"

	"github.com/tailscale/walk"
)

type jobState int

const (
	stWaiting jobState = iota
	stRunning
	stDone
	stError
	stCanceled
)

// jobOptions é uma "foto" das opções no momento em que o link entrou na fila.
type jobOptions struct {
	OutDir      string
	Quality     int
	Cookies     int
	Thumbnail   bool
	Subtitles   bool
	Playlist    bool
	LiveFromBeg bool
	ExtraArgs   string
}

// DlJob é um item da fila.
type DlJob struct {
	ID      int
	URL     string
	Title   string
	Status  string
	Percent string
	Speed   string
	ETA     string
	State   jobState
	Opts    jobOptions

	canceled bool
	cmd      *exec.Cmd
}

// JobTable alimenta a tabela da janela. Todos os campos dos jobs são
// protegidos por mu, porque as goroutines de download escrevem neles
// enquanto a janela lê.
type JobTable struct {
	walk.TableModelBase
	mu    sync.Mutex
	items []*DlJob
}

func (m *JobTable) RowCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

func (m *JobTable) Value(row, col int) interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row < 0 || row >= len(m.items) {
		return ""
	}
	j := m.items[row]
	switch col {
	case 0:
		if j.Title != "" {
			return j.Title
		}
		return j.URL
	case 1:
		return j.Status
	case 2:
		return j.Percent
	case 3:
		return j.Speed
	case 4:
		return j.ETA
	}
	return ""
}

func (m *JobTable) indexOf(j *DlJob) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, it := range m.items {
		if it == j {
			return i
		}
	}
	return -1
}
