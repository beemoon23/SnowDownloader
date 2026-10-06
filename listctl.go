package main

import (
	"fmt"
	"strings"

	"github.com/tailscale/walk"
)

// listCtl reúne as ações de uma lista da janela (downloads ou conversões):
// cancelar, tentar de novo, limpar, remover, abrir, mostrar na pasta, copiar.
// Os botões, o menu do botão direito e o duplo clique usam as mesmas funções.
type listCtl struct {
	a      *SnowApp
	t      *JobTable
	tv     *walk.TableView
	cancel func(j *DlJob) // cancela um item (cada lista tem o seu jeito)
	start  func()         // acorda a fila da lista
	before func()         // opcional: roda antes de reiniciar a fila (ex.: ler "simultâneos")
	what   string         // "link" ou "caminho": usado no texto de "copiar"
}

func (l *listCtl) selected() []*DlJob {
	var out []*DlJob
	l.t.mu.Lock()
	defer l.t.mu.Unlock()
	for _, i := range l.tv.SelectedIndexes() {
		if i >= 0 && i < len(l.t.items) {
			out = append(out, l.t.items[i])
		}
	}
	return out
}

// onSelection guarda quais linhas estão selecionadas (a tabela usa isso para pintar o Status).
func (l *listCtl) onSelection() {
	m := make(map[int]bool)
	for _, i := range l.tv.SelectedIndexes() {
		m[i] = true
	}
	l.t.sel = m
}

func (l *listCtl) cancelSelected() {
	for _, j := range l.selected() {
		l.cancel(j)
	}
}

func (l *listCtl) retrySelected() {
	sel := l.selected()
	l.t.mu.Lock()
	for _, j := range sel {
		if j.State == stError || j.State == stCanceled {
			j.State = stWaiting
			j.canceled = false
			j.Status = "Na fila"
			j.Percent, j.Speed, j.ETA = "", "", ""
			j.PctVal = 0
			j.FilePath = ""
		}
	}
	l.t.mu.Unlock()
	l.t.PublishRowsReset()
	if l.before != nil {
		l.before()
	}
	l.start()
}

func (l *listCtl) clearDone() {
	l.t.mu.Lock()
	kept := make([]*DlJob, 0, len(l.t.items))
	for _, j := range l.t.items {
		if j.State == stWaiting || j.State == stRunning {
			kept = append(kept, j)
		}
	}
	l.t.items = kept
	l.t.mu.Unlock()
	l.t.sel = map[int]bool{}
	l.t.PublishRowsReset()
}

func (l *listCtl) removeSelected() {
	sel := l.selected()
	if len(sel) == 0 {
		return
	}
	drop := make(map[*DlJob]bool, len(sel))
	for _, j := range sel {
		l.cancel(j) // se estiver rodando, para antes de tirar
		drop[j] = true
	}
	l.t.mu.Lock()
	kept := make([]*DlJob, 0, len(l.t.items))
	for _, j := range l.t.items {
		if !drop[j] {
			kept = append(kept, j)
		}
	}
	l.t.items = kept
	l.t.mu.Unlock()
	l.t.sel = map[int]bool{}
	l.t.PublishRowsReset()
}

// firstFile devolve o arquivo pronto do primeiro item selecionado, ou ""
// (e já avisa na barra de status) se não der para usar.
func (l *listCtl) firstFile() string {
	sel := l.selected()
	if len(sel) == 0 {
		return ""
	}
	l.t.mu.Lock()
	p, st := sel[0].FilePath, sel[0].State
	l.t.mu.Unlock()
	switch {
	case st != stDone:
		l.a.statusLB.SetText("Esse item ainda não terminou.")
		return ""
	case p == "" || !fileExists(p):
		l.a.statusLB.SetText("Não encontrei o arquivo (foi movido ou apagado?).")
		return ""
	}
	return p
}

func (l *listCtl) openSelected() {
	if p := l.firstFile(); p != "" {
		openFile(p)
	}
}

func (l *listCtl) showSelected() {
	if p := l.firstFile(); p != "" {
		showInFolder(p)
	}
}

func (l *listCtl) copyLinks() {
	sel := l.selected()
	if len(sel) == 0 {
		return
	}
	items := make([]string, 0, len(sel))
	for _, j := range sel {
		items = append(items, j.URL)
	}
	_ = walk.Clipboard().SetText(strings.Join(items, "\r\n"))
	l.a.statusLB.SetText(fmt.Sprintf("%d %s(s) copiado(s).", len(items), l.what))
}
