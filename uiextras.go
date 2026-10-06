package main

import (
	"github.com/tailscale/walk"
)

// Cores usadas na tabela.
var (
	colRunning  = walk.RGB(30, 100, 200)
	colDone     = walk.RGB(0, 135, 70)
	colError    = walk.RGB(200, 30, 30)
	colCanceled = walk.RGB(130, 130, 130)

	barTrack   = walk.RGB(215, 220, 228)
	barRunning = walk.RGB(50, 130, 230)
	barDone    = walk.RGB(60, 170, 95)
	barText    = walk.RGB(30, 30, 30)
)

// styleCell é chamado pela tabela para cada célula: aqui colorimos o Status
// e desenhamos a barra de progresso.
func (a *SnowApp) styleCell(t *JobTable, style *walk.CellStyle) {
	row, col := style.Row(), style.Col()
	if row < 0 || col < 0 {
		return
	}

	t.mu.Lock()
	if row >= len(t.items) {
		t.mu.Unlock()
		return
	}
	j := t.items[row]
	state, pct, text := j.State, j.PctVal, j.Percent
	t.mu.Unlock()

	switch col {
	case 1: // Status
		if t.sel[row] {
			return // linha selecionada: deixa o texto branco padrão
		}
		switch state {
		case stRunning:
			style.TextColor = colRunning
		case stDone:
			style.TextColor = colDone
		case stError:
			style.TextColor = colError
		case stCanceled:
			style.TextColor = colCanceled
		}
	case 2: // Progresso
		a.drawProgress(style, state, pct, text)
	}
}

// drawProgress desenha a célula inteira (fundo, barra e texto). Quando a
// célula pega o Canvas, a tabela deixa de desenhar o padrão, então tudo
// precisa ser pintado aqui.
func (a *SnowApp) drawProgress(style *walk.CellStyle, state jobState, pct float64, text string) {
	if a.cellFont == nil {
		return
	}
	b := style.BoundsPixels()
	if b.Width <= 0 || b.Height <= 0 {
		return // chamada "de consulta", sem desenho
	}
	canvas := style.Canvas()
	if canvas == nil {
		return
	}

	// fundo da célula
	if br, err := walk.NewSolidColorBrush(style.BackgroundColor); err == nil {
		_ = canvas.FillRectanglePixels(br, b)
		br.Dispose()
	}

	textColor := style.TextColor
	hasBar := pct >= 0 && (state == stRunning || state == stDone)
	if state == stDone {
		pct = 100
		text = "100%"
	}

	if hasBar {
		padX := 6
		padY := b.Height / 5
		track := walk.Rectangle{
			X:      b.X + padX,
			Y:      b.Y + padY,
			Width:  b.Width - 2*padX,
			Height: b.Height - 2*padY,
		}
		if track.Width > 2 && track.Height > 2 {
			if br, err := walk.NewSolidColorBrush(barTrack); err == nil {
				_ = canvas.FillRectanglePixels(br, track)
				br.Dispose()
			}

			w := int(float64(track.Width) * pct / 100.0)
			if w > track.Width {
				w = track.Width
			}
			if w > 0 {
				fillColor := barRunning
				if state == stDone {
					fillColor = barDone
				}
				fill := walk.Rectangle{X: track.X, Y: track.Y, Width: w, Height: track.Height}
				if br, err := walk.NewSolidColorBrush(fillColor); err == nil {
					_ = canvas.FillRectanglePixels(br, fill)
					br.Dispose()
				}
			}
			textColor = barText
		}
	}

	if text != "" {
		_ = canvas.DrawTextPixels(text, a.cellFont, textColor, b,
			walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}
}

// loadAppIcon carrega o ícone embutido no .exe (a ferramenta rsrc o grava
// com o número 2 quando o manifesto vem primeiro). Se não achar, usa o
// ícone padrão do Windows.
func loadAppIcon() *walk.Icon {
	for _, id := range []int{2, 7} {
		if ic, err := walk.NewIconFromResourceId(id); err == nil && ic != nil {
			return ic
		}
	}
	return walk.IconApplication()
}

// notify mostra um balão do Windows na bandeja do sistema.
func (a *SnowApp) notify(title, msg string, isErr bool) {
	a.ui(func() {
		if a.ni == nil {
			return
		}
		if isErr {
			_ = a.ni.ShowError(title, msg)
		} else {
			_ = a.ni.ShowInfo(title, msg)
		}
	})
}
