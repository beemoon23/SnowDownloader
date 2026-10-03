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
	hist     *History

	ni         *walk.NotifyIcon // ícone/balões na bandeja
	cellFont   *walk.Font       // fonte usada para desenhar a barra de progresso
	selRows    map[int]bool     // linhas selecionadas (só a thread da janela mexe)
	trayMin    bool             // minimizar para a bandeja?
	trayHinted bool             // já mostrou o aviso "continua rodando na bandeja"?
	persistFn  func()           // grava as opções da tela em disco

	ytBusy int32 // 1 enquanto o yt-dlp está sendo atualizado

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

	cleanupOldExe() // apaga restos de uma atualização anterior

	a := &SnowApp{
		table:    &JobTable{},
		tools:    newToolPaths(),
		settings: loadSettings(),
		hist:     loadHistory(),
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

// filterNew tira dos links os repetidos e os que já estão na lista
// (esperando, baixando ou concluídos). Devolve quantos foram ignorados.
func (a *SnowApp) filterNew(urls []string) ([]string, int) {
	inList := map[string]bool{}
	a.table.mu.Lock()
	for _, it := range a.table.items {
		if it.State != stError && it.State != stCanceled {
			inList[normURL(it.URL)] = true
		}
	}
	a.table.mu.Unlock()

	var out []string
	skipped := 0
	for _, u := range urls {
		k := normURL(u)
		if inList[k] {
			skipped++
			continue
		}
		inList[k] = true
		out = append(out, u)
	}
	return out, skipped
}

// askHistory avisa dos links que já foram baixados antes e pergunta se
// quer baixar de novo. Devolve os links que devem seguir para a fila.
func (a *SnowApp) askHistory(urls []string) []string {
	dups := map[string]bool{}
	var lines []string
	for _, u := range urls {
		if e, ok := a.hist.Get(u); ok {
			dups[normURL(u)] = true
			name := e.Title
			if name == "" {
				name = u
			}
			lines = append(lines, fmt.Sprintf("• %s (%s)", shorten(name, 60), time.Unix(e.When, 0).Format("02/01/2006")))
		}
	}
	if len(dups) == 0 {
		return urls
	}
	if len(lines) > 8 {
		extra := len(lines) - 8
		lines = append(lines[:8], fmt.Sprintf("… e mais %d", extra))
	}
	msg := fmt.Sprintf("%d link(s) já foi(ram) baixado(s) antes:\n\n%s\n\nBaixar de novo mesmo assim?",
		len(dups), strings.Join(lines, "\n"))
	if walk.MsgBox(a.mw, appTitle, msg, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes {
		return urls
	}

	var out []string
	for _, u := range urls {
		if !dups[normURL(u)] {
			out = append(out, u)
		}
	}
	return out
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
		secCK     *walk.CheckBox
		secFrom   *walk.LineEdit
		secTo     *walk.LineEdit
		trayCK    *walk.CheckBox
		tv        *walk.TableView
		optsGB    *walk.GroupBox

		showOptsCK *walk.CheckBox
	)

	s := a.settings
	a.maxConcurrent = s.Concurrency + 1
	a.trayMin = s.TrayMin

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

	// persist grava as opções. Parte das configurações atuais, assim campos
	// que não têm controle na tela (ex.: data da última atualização do
	// yt-dlp) não se perdem.
	persist := func() {
		o := currentOpts()
		st := a.settings
		st.OutDir = o.OutDir
		st.Quality = o.Quality
		st.Cookies = o.Cookies
		st.Concurrency = concCB.CurrentIndex()
		st.Thumbnail = o.Thumbnail
		st.Subtitles = o.Subtitles
		st.Playlist = o.Playlist
		st.LiveFromBeg = o.LiveFromBeg
		st.ExtraArgs = o.ExtraArgs
		st.TrayMin = trayCK.Checked()
		a.settings = st
		saveSettings(st)
	}
	a.persistFn = persist

	// enqueue coloca os links na fila e já começa a baixar.
	// Devolve false quando algo impediu (pasta vazia, trecho inválido): nesse
	// caso o texto da caixa é mantido para a pessoa corrigir.
	enqueue := func(urls []string) bool {
		opts := currentOpts()
		if opts.OutDir == "" {
			walk.MsgBox(a.mw, appTitle, "Escolha a pasta de destino (em \"Mostrar opções\").", walk.MsgBoxIconInformation)
			return false
		}
		sec, err := sectionArg(secCK.Checked(), secFrom.Text(), secTo.Text())
		if err != nil {
			walk.MsgBox(a.mw, appTitle, err.Error(), walk.MsgBoxIconInformation)
			return false
		}
		opts.Section = sec

		urls, skipped := a.filterNew(urls)
		if len(urls) > 0 {
			urls = a.askHistory(urls)
		}
		if len(urls) == 0 {
			if skipped > 0 {
				a.statusLB.SetText("Esse link já está na lista.")
			} else {
				a.statusLB.SetText("Nada novo para baixar.")
			}
			return true
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
		persist()
		msg := fmt.Sprintf("%d link(s) na fila — baixando…", len(urls))
		if skipped > 0 {
			msg += fmt.Sprintf(" (%d repetido(s) ignorado(s))", skipped)
		}
		a.statusLB.SetText(msg)
		a.startQueue()
		return true
	}

	// addURLs lê os links da caixa de texto.
	// silent=true é o modo "colou e foi": não abre janelinhas de aviso.
	addURLs := func(silent bool) {
		if strings.TrimSpace(urlEdit.Text()) == "" {
			return
		}
		urls := extractURLs(urlEdit.Text())
		if len(urls) == 0 {
			if silent {
				a.statusLB.SetText("Não achei um link (http:// ou https://) no que você colou.")
			} else {
				walk.MsgBox(a.mw, appTitle, "Cole pelo menos um link (começando com http:// ou https://).", walk.MsgBoxIconInformation)
			}
			return
		}
		if enqueue(urls) {
			urlEdit.SetText("")
		}
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

	// Arrastar arquivos (.txt com links, atalhos .url...) para a janela.
	dropFiles := func(files []string) {
		var found []string
		for _, f := range files {
			st, err := os.Stat(f)
			if err != nil || st.IsDir() || st.Size() > 4<<20 {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			found = append(found, extractURLs(string(b))...)
		}
		if len(found) == 0 {
			a.statusLB.SetText("Não achei links nos arquivos arrastados (use um .txt com um link por linha).")
			return
		}
		enqueue(found)
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

	clearHistory := func() {
		n := a.hist.Count()
		if n == 0 {
			walk.MsgBox(a.mw, appTitle, "O histórico já está vazio.", walk.MsgBoxIconInformation)
			return
		}
		msg := fmt.Sprintf("Apagar o histórico de %d vídeo(s) baixado(s)?\n\nOs arquivos baixados não são apagados, só a memória de \"já baixei esse link\".", n)
		if walk.MsgBox(a.mw, appTitle, msg, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes {
			a.hist.Clear()
			a.statusLB.SetText("Histórico apagado.")
		}
	}

	err := MainWindow{
		AssignTo:    &a.mw,
		Title:       appTitle + versionSuffix() + " — baixe vídeos, lives, reels e áudios",
		MinSize:     Size{Width: 820, Height: 480},
		Size:        Size{Width: 1000, Height: 660},
		Font:        Font{Family: "Segoe UI", PointSize: 10},
		OnDropFiles: dropFiles,
		Layout: VBox{
			Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 10},
			Spacing: 8,
		},
		Children: []Widget{
			GroupBox{
				Title:  "Cole o link (de qualquer site) e o download começa sozinho — ou arraste um .txt com vários links",
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
							CheckBox{AssignTo: &secCK, Text: "Baixar só um trecho do vídeo:"},
							Label{Text: "de"},
							LineEdit{AssignTo: &secFrom, CueBanner: "0:00", MaxSize: Size{Width: 80}},
							Label{Text: "até"},
							LineEdit{AssignTo: &secTo, CueBanner: "fim", MaxSize: Size{Width: 80}},
							Label{Text: "(ex.: 1:30 e 5:00; vale para os próximos links)"},
							HSpacer{},
						},
					},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							CheckBox{
								AssignTo: &trayCK,
								Text:     "Minimizar para a bandeja (perto do relógio)",
								Checked:  s.TrayMin,
								OnCheckedChanged: func() {
									a.trayMin = trayCK.Checked()
								},
							},
							HSpacer{},
							PushButton{Text: "Limpar histórico", OnClicked: clearHistory},
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
										a.settings.LastYtDlpCheck = time.Now().Unix()
										persist()
										walk.MsgBox(a.mw, appTitle, "yt-dlp está na versão "+ver+".\n\n"+out, walk.MsgBoxIconInformation)
									}
								})
								a.setStatus("yt-dlp " + ver + " pronto.")
							}()
						},
					},
					PushButton{
						Text:      "Atualizar app",
						OnClicked: func() { a.checkAppUpdate(true) },
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
	a.setupTray(icon)

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

	// Prepara yt-dlp/ffmpeg em segundo plano (baixa só na primeira vez) e,
	// depois, faz as checagens automáticas de atualização.
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
		a.autoChecks()
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
