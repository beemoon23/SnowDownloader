package main

import (
	"github.com/tailscale/walk"
	"github.com/tailscale/win"
)

// setupTray cria o ícone da bandeja (perto do relógio), com menu e o
// comportamento de "minimizar para a bandeja".
func (a *SnowApp) setupTray(icon *walk.Icon) {
	ni, err := walk.NewNotifyIcon()
	if err != nil {
		return
	}
	_ = ni.SetIcon(icon)
	_ = ni.SetToolTip(appTitle)

	openAct := walk.NewAction()
	_ = openAct.SetText("Abrir o SnowDownloader")
	openAct.Triggered().Attach(a.restoreFromTray)
	_ = ni.ContextMenu().Actions().Add(openAct)

	exitAct := walk.NewAction()
	_ = exitAct.SetText("Sair")
	exitAct.Triggered().Attach(func() { _ = a.mw.Close() })
	_ = ni.ContextMenu().Actions().Add(exitAct)

	// Clique com o botão esquerdo no ícone: traz a janela de volta.
	ni.MouseDown().Attach(func(x, y int, b walk.MouseButton) {
		if b == walk.LeftButton {
			a.restoreFromTray()
		}
	})

	_ = ni.SetVisible(true)
	a.ni = ni

	// Ao minimizar, some da barra de tarefas e fica só na bandeja.
	a.mw.SizeChanged().Attach(func() {
		if !a.trayMin || !win.IsIconic(a.mw.Handle()) {
			return
		}
		a.mw.Synchronize(func() {
			if !a.trayMin || !win.IsIconic(a.mw.Handle()) {
				return
			}
			a.mw.Hide()
			if !a.trayHinted {
				a.trayHinted = true
				a.notify("SnowDownloader continua rodando",
					"Os downloads seguem aqui na bandeja. Clique no ícone para abrir.", false)
			}
		})
	})
}

// restoreFromTray mostra a janela de novo.
func (a *SnowApp) restoreFromTray() {
	hwnd := a.mw.Handle()
	a.mw.SetVisible(true)
	win.ShowWindow(hwnd, win.SW_RESTORE)
	win.SetForegroundWindow(hwnd)
}
