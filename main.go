package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

const appTitle = "SnowDownloader"

// SnowApp junta janela, fila e ferramentas.
type SnowApp struct {
	mw       *walk.MainWindow
	statusLB *walk.Label
	table    *JobTable
	tools    ToolPaths
	settings AppSettings

	ni       *walk.NotifyIcon // ícone/balões na bandeja
	cellFont *walk.Font       // fonte usada para desenhar a barra de progresso
	selRows  map[int]bool     // linhas selecionadas (só a thread da janela mexe)

	qmu           sync.Mutex
	queueRunning  bool
	active        int
	maxConcurrent int
	nextID        int
}

func init() {
	// A interface do Windows exige que tudo rode sempre na mesma thread.
	runtime.LockOSThread()
}

func main() {
	// walk.InitApp() TEM que ser a primeira chamada da biblioteca de janela.
	// É ela que registra a classe da janela principal; sem isso o Windows
	// responde "CreateWindowEx" e a janela nunca abre.
	if _, err := walk.InitApp(); err != nil {
		walk.MsgBox(nil, appTitle, "Erro ao iniciar a interface:\n\n"+err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}

	a := &SnowApp{
		table:    &JobTable{},
		tools:    newToolPaths(),
		settings: loadSettings(),
		selRows:  map[int]bool{},
	}
	if err := a.run(); err != nil {
		walk.MsgBox(nil, appTitle, "Erro ao abrir a janela:\n\n"+err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
}

// ui roda uma função na thread da janela (obrigatório para mexer em widgets).
func (a *SnowApp) ui(f func()) {
	if a.mw != nil {
		a.mw.Synchronize(f)
	}
}

func (a *SnowApp) setStatus(s string) {
	a.ui(func() {
		if a.statusLB != nil {
			a.statusLB.SetText(s)
		}
	})
}

// refresh redesenha a linha de um job na tabela.
func (a *SnowApp) refresh(j *DlJob) {
	a.ui(func() {
		if i := a.table.indexOf(j); i >= 0 {
			a.table.PublishRowChanged(i)
		}
	})
}

func (a *SnowApp) run() error {
	var (
		urlEdit   *walk.TextEdit
		outEdit   *walk.LineEdit
		extraEdit *walk.LineEdit
		qualityCB *walk.ComboBox
		cookiesCB *walk.ComboBox
		concCB    *walk.ComboBox
		thumbCK   *walk.CheckBox
		subsCK    *walk.CheckBox
		playlCK   *walk.CheckBox
		liveCK    *walk.CheckBox
		tv        *walk.TableView
		optsGB    *walk.GroupBox

		showOptsCK *walk.CheckBox
	)

	s := a.settings
	a.maxConcurrent = s.Concurrency + 1

	// Lê as opções atuais da tela.
	currentOpts := func() jobOptions {
		return jobOptions{
			OutDir:      strings.TrimSpace(outEdit.Text()),
			Quality:     qualityCB.CurrentIndex(),
			Cookies:     cookiesCB.CurrentIndex(),
			Thumbnail:   thumbCK.Checked(),
			Subtitles:   subsCK.Checked(),
			Playlist:    playlCK.Checked(),
			LiveFromBeg: liveCK.Checked(),
			ExtraArgs:   strings.TrimSpace(extraEdit.Text()),
		}
	}

	persist := func() {
		o := currentOpts()
		saveSettings(AppSettings{
			OutDir:      o.OutDir,
			Quality:     o.Quality,
			Cookies:     o.Cookies,
			Concurrency: concCB.CurrentIndex(),
			Thumbnail:   o.Thumbnail,
			Subtitles:   o.Subtitles,
			Playlist:    o.Playlist,
			LiveFromBeg: o.LiveFromBeg,
			ExtraArgs:   o.ExtraArgs,
		})
	}

	// addURLs lê os links da caixa, coloca na fila e já começa a baixar.
	// silent=true é o modo "colou e foi": não abre janelinhas de aviso.
	addURLs := func(silent bool) {
		if strings.TrimSpace(urlEdit.Text()) == "" {
			return
		}
		raw := strings.ReplaceAll(urlEdit.Text(), "\r", "")
		var urls []string
		for _, line := range strings.Split(raw, "\n") {
			for _, f := range strings.Fields(line) {
				if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
					urls = append(urls, f)
				}
			}
		}
		if len(urls) == 0 {
			if silent {
				a.statusLB.SetText("Não achei um link (http:// ou https://) no que você colou.")
			} else {
				walk.MsgBox(a.mw, appTitle, "Cole pelo menos um link (começando com http:// ou https://).", walk.MsgBoxIconInformation)
			}
			return
		}
		opts := currentOpts()
		if opts.OutDir == "" {
			walk.MsgBox(a.mw, appTitle, "Escolha a pasta de destino (em \"Mostrar opções\").", walk.MsgBoxIconInformation)
			return
		}

		a.maxConcurrent = concCB.CurrentIndex() + 1

		a.table.mu.Lock()
		from := len(a.table.items)
		for _, u := range urls {
			a.nextID++
			a.table.items = append(a.table.items, &DlJob{
				ID:     a.nextID,
				URL:    u,
				Status: "Na fila",
				Opts:   opts,
			})
		}
		to := len(a.table.items) - 1
		a.table.mu.Unlock()

		a.table.PublishRowsInserted(from, to)
		urlEdit.SetText("")
		persist()
		a.statusLB.SetText(fmt.Sprintf("%d link(s) na fila — baixando…", len(urls)))
		a.startQueue()
	}

	addAndStart := func() { addURLs(false) }

	// Botão "Colar e baixar": pega o que está copiado e já começa.
	pasteAndGo := func() {
		txt, err := walk.Clipboard().Text()
		if err != nil || strings.TrimSpace(txt) == "" {
			a.statusLB.SetText("Nada copiado ainda. Copie um link primeiro.")
			return
		}
		cur := strings.TrimRight(urlEdit.Text(), "\r\n ")
		if cur != "" {
			cur += "\r\n"
		}
		urlEdit.SetText(cur + strings.TrimSpace(txt))
		addURLs(false)
	}

	selectedJobs := func() []*DlJob {
		var out []*DlJob
		a.table.mu.Lock()
		defer a.table.mu.Unlock()
		for _, i := range tv.SelectedIndexes() {
			if i >= 0 && i < len(a.table.items) {
				out = append(out, a.table.items[i])
			}
		}
		return out
	}

	// ---- ações da lista (botões, menu do botão direito e duplo clique) ----

	cancelSelected := func() {
		for _, j := range selectedJobs() {
			a.cancelJob(j)
		}
	}

	retrySelected := func() {
		sel := selectedJobs()
		a.table.mu.Lock()
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
		a.table.mu.Unlock()
		a.table.PublishRowsReset()
		a.maxConcurrent = concCB.CurrentIndex() + 1
		a.startQueue()
	}

	clearDone := func() {
		a.table.mu.Lock()
		kept := make([]*DlJob, 0, len(a.table.items))
		for _, j := range a.table.items {
			if j.State == stWaiting || j.State == stRunning {
				kept = append(kept, j)
			}
		}
		a.table.items = kept
		a.table.mu.Unlock()
		a.selRows = map[int]bool{}
		a.table.PublishRowsReset()
	}

	removeSelected := func() {
		sel := selectedJobs()
		if len(sel) == 0 {
			return
		}
		drop := make(map[*DlJob]bool, len(sel))
		for _, j := range sel {
			a.cancelJob(j) // se estiver baixando, para antes de tirar
			drop[j] = true
		}
		a.table.mu.Lock()
		kept := make([]*DlJob, 0, len(a.table.items))
		for _, j := range a.table.items {
			if !drop[j] {
				kept = append(kept, j)
			}
		}
		a.table.items = kept
		a.table.mu.Unlock()
		a.selRows = map[int]bool{}
		a.table.PublishRowsReset()
	}

	// fileOfFirstSelected devolve o arquivo do primeiro item selecionado,
	// ou "" (e já avisa na barra de status) se não der para usar.
	fileOfFirstSelected := func() string {
		sel := selectedJobs()
		if len(sel) == 0 {
			return ""
		}
		a.table.mu.Lock()
		p, st := sel[0].FilePath, sel[0].State
		a.table.mu.Unlock()
		switch {
		case st != stDone:
			a.statusLB.SetText("Esse download ainda não terminou.")
			return ""
		case p == "" || !fileExists(p):
			a.statusLB.SetText("Não encontrei o arquivo (foi movido ou apagado?).")
			return ""
		}
		return p
	}

	openSelected := func() {
		if p := fileOfFirstSelected(); p != "" {
			openFile(p)
		}
	}

	showSelected := func() {
		if p := fileOfFirstSelected(); p != "" {
			showInFolder(p)
		}
	}

	copyLinks := func() {
		sel := selectedJobs()
		if len(sel) == 0 {
			return
		}
		urls := make([]string, 0, len(sel))
		for _, j := range sel {
			urls = append(urls, j.URL)
		}
		_ = walk.Clipboard().SetText(strings.Join(urls, "\r\n"))
		a.statusLB.SetText(fmt.Sprintf("%d link(s) copiado(s).", len(urls)))
	}

	err := MainWindow{
		AssignTo: &a.mw,
		Title:    appTitle + " — baixe vídeos, lives, reels e áudios",
		MinSize:  Size{Width: 820, Height: 480},
		Size:     Size{Width: 1000, Height: 660},
		Font:     Font{Family: "Segoe UI", PointSize: 10},
		Layout: VBox{
			Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 10},
			Spacing: 8,
		},
		Children: []Widget{
			GroupBox{
				Title:  "Cole o link (de qualquer site) e o download começa sozinho",
				Layout: VBox{Margins: Margins{Left: 10, Top: 8, Right: 10, Bottom: 10}, Spacing: 8},
				Children: []Widget{
					TextEdit{
						AssignTo: &urlEdit,
						VScroll:  true,
						MinSize:  Size{Height: 56},
						MaxSize:  Size{Height: 90},
					},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							PushButton{
								Text:      "📋  Colar e baixar",
								MinSize:   Size{Width: 170, Height: 34},
								OnClicked: pasteAndGo,
							},
							HSpacer{},
							CheckBox{
								AssignTo: &showOptsCK,
								Text:     "Mostrar opções",
								OnCheckedChanged: func() {
									optsGB.SetVisible(showOptsCK.Checked())
								},
							},
							PushButton{
								Text:      "▶  Baixar",
								MinSize:   Size{Width: 130, Height: 34},
								OnClicked: addAndStart,
							},
						},
					},
				},
			},
			GroupBox{
				AssignTo: &optsGB,
				Visible:  false,
				Title:    "Opções",
				Layout:   VBox{Margins: Margins{Left: 10, Top: 8, Right: 10, Bottom: 10}, Spacing: 8},
				Children: []Widget{
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							Label{Text: "Qualidade:"},
							ComboBox{
								AssignTo:     &qualityCB,
								Model:        qualityNames(),
								CurrentIndex: clamp(s.Quality, len(qualityOptions)),
								MinSize:      Size{Width: 300},
							},
							Label{Text: "Cookies do navegador:"},
							ComboBox{
								AssignTo:     &cookiesCB,
								Model:        cookieNames(),
								CurrentIndex: clamp(s.Cookies, len(cookieBrowsers)),
							},
							Label{Text: "Simultâneos:"},
							ComboBox{
								AssignTo:     &concCB,
								Model:        []string{"1", "2", "3", "4"},
								CurrentIndex: clamp(s.Concurrency, 4),
							},
						},
					},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							Label{Text: "Pasta de destino:"},
							LineEdit{AssignTo: &outEdit, Text: s.OutDir},
							PushButton{
								Text: "Escolher…",
								OnClicked: func() {
									dlg := new(walk.FileDialog)
									dlg.Title = "Escolha a pasta de destino"
									dlg.FilePath = outEdit.Text()
									if ok, err := dlg.ShowBrowseFolder(a.mw); err == nil && ok {
										outEdit.SetText(dlg.FilePath)
									}
								},
							},
						},
					},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							CheckBox{AssignTo: &thumbCK, Text: "Miniatura e metadados no arquivo", Checked: s.Thumbnail},
							CheckBox{AssignTo: &subsCK, Text: "Legendas (PT/EN)", Checked: s.Subtitles},
							CheckBox{AssignTo: &playlCK, Text: "Baixar playlist / canal inteiro", Checked: s.Playlist},
							CheckBox{AssignTo: &liveCK, Text: "Live: gravar desde o início", Checked: s.LiveFromBeg},
							HSpacer{},
						},
					},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							Label{Text: "Argumentos extras do yt-dlp (avançado):"},
							LineEdit{AssignTo: &extraEdit, Text: s.ExtraArgs},
						},
					},
				},
			},
			TableView{
				AssignTo:            &tv,
				AlternatingRowBG:    true,
				MultiSelection:      true,
				ColumnsOrderable:    false,
				LastColumnStretched: true,
				CustomRowHeight:     28,
				Model:               a.table,
				StyleCell:           a.styleCell,
				OnItemActivated:     openSelected, // duplo clique / Enter abre o vídeo
				OnSelectedIndexesChanged: func() {
					m := make(map[int]bool)
					for _, i := range tv.SelectedIndexes() {
						m[i] = true
					}
					a.selRows = m
				},
				ContextMenuItems: []MenuItem{
					Action{Text: "Abrir arquivo", OnTriggered: openSelected},
					Action{Text: "Mostrar na pasta", OnTriggered: showSelected},
					Action{Text: "Copiar link", OnTriggered: copyLinks},
					Separator{},
					Action{Text: "Cancelar", OnTriggered: cancelSelected},
					Action{Text: "Tentar de novo", OnTriggered: retrySelected},
					Action{Text: "Remover da lista", OnTriggered: removeSelected},
				},
				Columns: []TableViewColumn{
					{Title: "Título / link", Width: 380},
					{Title: "Status", Width: 220},
					{Title: "Progresso", Width: 140},
					{Title: "Velocidade", Width: 100},
					{Title: "Restante", Width: 80},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "Cancelar selecionados", OnClicked: cancelSelected},
					PushButton{Text: "Tentar de novo", OnClicked: retrySelected},
					PushButton{Text: "Limpar concluídos", OnClicked: clearDone},
					HSpacer{},
					PushButton{
						Text: "Abrir pasta",
						OnClicked: func() {
							dir := strings.TrimSpace(outEdit.Text())
							_ = os.MkdirAll(dir, 0o755)
							openFolder(dir)
						},
					},
					PushButton{
						Text: "Atualizar yt-dlp",
						OnClicked: func() {
							a.qmu.Lock()
							busy := a.active > 0
							a.qmu.Unlock()
							if busy {
								walk.MsgBox(a.mw, appTitle, "Espere os downloads terminarem (ou cancele) antes de atualizar.", walk.MsgBoxIconInformation)
								return
							}
							if !fileExists(a.tools.YtDlp) {
								walk.MsgBox(a.mw, appTitle, "O yt-dlp ainda está sendo baixado. Tente em instantes.", walk.MsgBoxIconInformation)
								return
							}
							a.setStatus("Atualizando yt-dlp…")
							go func() {
								out, err := a.updateYtDlp()
								ver := a.ytDlpVersion()
								a.ui(func() {
									if err != nil {
										walk.MsgBox(a.mw, appTitle, "Não consegui atualizar:\n\n"+out, walk.MsgBoxIconError)
									} else {
										walk.MsgBox(a.mw, appTitle, "yt-dlp está na versão "+ver+".\n\n"+out, walk.MsgBoxIconInformation)
									}
								})
								a.setStatus("yt-dlp " + ver + " pronto.")
							}()
						},
					},
				},
			},
			Label{AssignTo: &a.statusLB, Text: "Preparando…"},
		},
	}.Create()
	if err != nil {
		return err
	}

	// Fonte usada para desenhar a barra de progresso na tabela.
	a.cellFont = tv.Font()

	// Ícone da janela e da bandeja (vem embutido no .exe pelo build).
	icon := loadAppIcon()
	_ = a.mw.SetIcon(icon)
	if ni, err := walk.NewNotifyIcon(); err == nil {
		_ = ni.SetIcon(icon)
		_ = ni.SetToolTip(appTitle)
		_ = ni.SetVisible(true)
		a.ni = ni
	}

	// "Colou e foi": quando entra um bloco grande de texto de uma vez (colar),
	// espera um instante e começa a baixar sozinho. Digitar à mão não dispara.
	prevLen := 0
	var pasteGen int64
	urlEdit.TextChanged().Attach(func() {
		n := len(urlEdit.Text())
		grew := n - prevLen
		prevLen = n
		if grew < 8 {
			return
		}
		gen := atomic.AddInt64(&pasteGen, 1)
		time.AfterFunc(350*time.Millisecond, func() {
			if atomic.LoadInt64(&pasteGen) != gen {
				return // chegou mais texto, esse disparo ficou velho
			}
			a.ui(func() { addURLs(true) })
		})
	})
	urlEdit.SetFocus()

	a.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		persist()
		a.table.mu.Lock()
		items := append([]*DlJob(nil), a.table.items...)
		a.table.mu.Unlock()
		for _, j := range items {
			a.cancelJob(j) // não deixa yt-dlp/ffmpeg órfãos rodando
		}
	})

	// Prepara yt-dlp/ffmpeg em segundo plano (baixa só na primeira vez).
	go func() {
		if err := a.ensureTools(); err != nil {
			a.setStatus("Erro ao preparar ferramentas: " + err.Error())
			a.ui(func() {
				walk.MsgBox(a.mw, appTitle,
					"Não consegui baixar o yt-dlp/ffmpeg:\n\n"+err.Error()+
						"\n\nVerifique a internet/antivírus e reabra o programa.",
					walk.MsgBoxIconError)
			})
			return
		}
		a.setStatus("yt-dlp " + a.ytDlpVersion() + " pronto. É só colar o link.")
	}()

	// Nesta versão do walk o loop principal roda via walk.App().Run().
	walk.App().Run()

	// Tira o ícone da bandeja ao fechar (senão ele fica "fantasma").
	if a.ni != nil {
		_ = a.ni.Dispose()
	}
	return nil
}

func clamp(i, n int) int {
	if i < 0 || i >= n {
		return 0
	}
	return i
}
