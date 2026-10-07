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

// SnowApp junta janela, filas e ferramentas.
type SnowApp struct {
	mw       *walk.MainWindow
	statusLB *walk.Label
	table    *JobTable // lista da aba Baixar
	ctable   *JobTable // lista da aba Converter
	tools    ToolPaths
	settings AppSettings
	hist     *History

	ni         *walk.NotifyIcon // ícone/balões na bandeja
	cellFont   *walk.Font       // fonte usada para desenhar a barra de progresso
	trayMin    bool             // minimizar para a bandeja?
	trayHinted bool             // já mostrou o aviso "continua rodando na bandeja"?
	persistFn  func()           // grava as opções da tela em disco

	ytBusy int32 // 1 enquanto o yt-dlp está sendo atualizado

	// fila de downloads
	qmu           sync.Mutex
	queueRunning  bool
	active        int
	maxConcurrent int
	nextID        int

	// fila de conversões
	cqmu      sync.Mutex
	cqRunning bool
	cnextID   int
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
		table:    &JobTable{sel: map[int]bool{}},
		ctable:   &JobTable{sel: map[int]bool{}},
		tools:    newToolPaths(),
		settings: loadSettings(),
		hist:     loadHistory(),
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

// refreshIn redesenha a linha de um item na tabela indicada.
func (a *SnowApp) refreshIn(t *JobTable, j *DlJob) {
	a.ui(func() {
		if i := t.indexOf(j); i >= 0 {
			t.PublishRowChanged(i)
		}
	})
}

// refresh redesenha a linha de um download.
func (a *SnowApp) refresh(j *DlJob) {
	a.refreshIn(a.table, j)
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
		tabs *walk.TabWidget

		// aba Baixar
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
		optsGB    *walk.GroupBox

		showOptsCK *walk.CheckBox

		// aba Converter
		convCB        *walk.ComboBox
		convHintTE    *walk.TextEdit
		sameDirCK     *walk.CheckBox
		convOutEdit   *walk.LineEdit
		convBrowseBtn *walk.PushButton
	)

	s := a.settings
	a.maxConcurrent = s.Concurrency + 1
	a.trayMin = s.TrayMin

	// Controles das duas listas (ações dos botões, do menu do botão direito e do duplo clique).
	dl := &listCtl{a: a, t: a.table, cancel: a.cancelJob, start: a.startQueue, what: "link"}
	cv := &listCtl{a: a, t: a.ctable, cancel: a.cancelConv, start: a.startConvQueue, what: "caminho"}

	// Lê as opções atuais da tela (aba Baixar).
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
		st.ConvFormat = convCB.CurrentIndex()
		st.ConvSameDir = sameDirCK.Checked()
		st.ConvOutDir = strings.TrimSpace(convOutEdit.Text())
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

	// ---- aba Converter ----

	// addConvPaths põe arquivos (ou pastas) na fila de conversão com o formato escolhido.
	addConvPaths := func(paths []string) {
		if len(paths) == 0 {
			return
		}
		same := sameDirCK.Checked()
		outDir := strings.TrimSpace(convOutEdit.Text())
		if !same && outDir == "" {
			walk.MsgBox(a.mw, appTitle, "Escolha a pasta de saída (ou marque \"Salvar na mesma pasta do arquivo original\").", walk.MsgBoxIconInformation)
			return
		}
		idx := convCB.CurrentIndex()
		if idx < 0 || idx >= len(convFormats) {
			idx = 0
		}
		n := a.addConvFiles(paths, convOptions{Fmt: idx, SameDir: same, OutDir: outDir})
		if n == 0 {
			a.statusLB.SetText("Nenhum arquivo novo para converter (já estão na fila, ou a pasta não tem vídeo/áudio).")
			return
		}
		persist()
		a.statusLB.SetText(fmt.Sprintf("%d arquivo(s) na fila — convertendo para: %s", n, convFormats[idx].Name))
	}

	addConvDialog := func() {
		dlg := new(walk.FileDialog)
		dlg.Title = "Escolha os arquivos para converter"
		dlg.Filter = "Vídeo e áudio|*.mp4;*.mkv;*.mov;*.avi;*.webm;*.flv;*.wmv;*.m4v;*.3gp;*.ts;*.mpg;*.mpeg;*.mp3;*.m4a;*.wav;*.flac;*.ogg;*.opus;*.aac;*.wma|Todos os arquivos|*.*"
		if ok, err := dlg.ShowOpenMultiple(a.mw); err == nil && ok {
			addConvPaths(dlg.FilePaths)
		}
	}

	// Atalho do menu da aba Baixar: manda o vídeo baixado para o conversor.
	convertDownloaded := func() {
		p := dl.firstFile()
		if p == "" {
			return
		}
		_ = tabs.SetCurrentIndex(1)
		addConvPaths([]string{p})
	}

	// Arrastar arquivos para a janela: na aba Converter são os arquivos a
	// converter; na aba Baixar são .txt/atalhos com links.
	dropFiles := func(files []string) {
		if tabs.CurrentIndex() == 1 {
			addConvPaths(files)
			return
		}
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

	// before das listas de download: relê quantos downloads simultâneos.
	dl.before = func() { a.maxConcurrent = concCB.CurrentIndex() + 1 }

	// ---- páginas ----

	baixarPage := TabPage{
		Title:  "Baixar",
		Layout: VBox{MarginsZero: true, Spacing: 8},
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
						Layout:  HBox{MarginsZero: true},
						MaxSize: Size{Height: 40},
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
				StretchFactor:            10,
				AssignTo:                 &dl.tv,
				AlternatingRowBG:         true,
				MultiSelection:           true,
				ColumnsOrderable:         false,
				LastColumnStretched:      true,
				CustomRowHeight:          28,
				Model:                    a.table,
				StyleCell:                func(st *walk.CellStyle) { a.styleCell(a.table, st) },
				OnItemActivated:          dl.openSelected, // duplo clique / Enter abre o vídeo
				OnSelectedIndexesChanged: dl.onSelection,
				ContextMenuItems: []MenuItem{
					Action{Text: "Abrir arquivo", OnTriggered: dl.openSelected},
					Action{Text: "Mostrar na pasta", OnTriggered: dl.showSelected},
					Action{Text: "Converter este arquivo…", OnTriggered: convertDownloaded},
					Action{Text: "Copiar link", OnTriggered: dl.copyLinks},
					Separator{},
					Action{Text: "Cancelar", OnTriggered: dl.cancelSelected},
					Action{Text: "Tentar de novo", OnTriggered: dl.retrySelected},
					Action{Text: "Remover da lista", OnTriggered: dl.removeSelected},
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
					PushButton{Text: "Cancelar selecionados", OnClicked: dl.cancelSelected},
					PushButton{Text: "Tentar de novo", OnClicked: dl.retrySelected},
					PushButton{Text: "Limpar concluídos", OnClicked: dl.clearDone},
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
		},
	}

	converterPage := TabPage{
		Title:  "Converter",
		Layout: VBox{MarginsZero: true, Spacing: 8},
		Children: []Widget{
			GroupBox{
				Title:  "Converter arquivos que já estão no PC — escolha o formato e arraste os arquivos (ou uma pasta) para a janela",
				Layout: VBox{Margins: Margins{Left: 10, Top: 8, Right: 10, Bottom: 10}, Spacing: 8},
				Children: []Widget{
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							Label{Text: "Converter para:"},
							ComboBox{
								AssignTo:     &convCB,
								Model:        convNames(),
								CurrentIndex: clamp(s.ConvFormat, len(convFormats)),
								MinSize:      Size{Width: 380},
								OnCurrentIndexChanged: func() {
									i := convCB.CurrentIndex()
									if convHintTE != nil && i >= 0 && i < len(convFormats) {
										convHintTE.SetText(convFormats[i].Hint)
									}
								},
							},
							HSpacer{},
							PushButton{
								Text:      "📂  Adicionar arquivos…",
								MinSize:   Size{Width: 210, Height: 34},
								OnClicked: addConvDialog,
							},
						},
					},
					TextEdit{
						AssignTo: &convHintTE,
						ReadOnly: true,
						Text:     convFormats[clamp(s.ConvFormat, len(convFormats))].Hint,
						MinSize:  Size{Height: 40},
						MaxSize:  Size{Height: 52},
					},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							CheckBox{
								AssignTo: &sameDirCK,
								Text:     "Salvar na mesma pasta do arquivo original",
								Checked:  s.ConvSameDir,
								OnCheckedChanged: func() {
									on := !sameDirCK.Checked()
									convOutEdit.SetEnabled(on)
									convBrowseBtn.SetEnabled(on)
								},
							},
						},
					},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							Label{Text: "Pasta de saída:"},
							LineEdit{AssignTo: &convOutEdit, Text: s.ConvOutDir, Enabled: !s.ConvSameDir},
							PushButton{
								AssignTo: &convBrowseBtn,
								Text:     "Escolher…",
								Enabled:  !s.ConvSameDir,
								OnClicked: func() {
									dlg := new(walk.FileDialog)
									dlg.Title = "Escolha a pasta de saída"
									dlg.FilePath = convOutEdit.Text()
									if ok, err := dlg.ShowBrowseFolder(a.mw); err == nil && ok {
										convOutEdit.SetText(dlg.FilePath)
									}
								},
							},
						},
					},
					Label{Text: "O arquivo original nunca é apagado nem sobrescrito. O resultado ganha um nome novo se já existir um igual."},
				},
			},
			TableView{
				StretchFactor:            10,
				AssignTo:                 &cv.tv,
				AlternatingRowBG:         true,
				MultiSelection:           true,
				ColumnsOrderable:         false,
				LastColumnStretched:      true,
				CustomRowHeight:          28,
				Model:                    a.ctable,
				StyleCell:                func(st *walk.CellStyle) { a.styleCell(a.ctable, st) },
				OnItemActivated:          cv.openSelected, // duplo clique / Enter abre o arquivo convertido
				OnSelectedIndexesChanged: cv.onSelection,
				ContextMenuItems: []MenuItem{
					Action{Text: "Abrir arquivo convertido", OnTriggered: cv.openSelected},
					Action{Text: "Mostrar na pasta", OnTriggered: cv.showSelected},
					Action{Text: "Copiar caminho do original", OnTriggered: cv.copyLinks},
					Separator{},
					Action{Text: "Cancelar", OnTriggered: cv.cancelSelected},
					Action{Text: "Tentar de novo", OnTriggered: cv.retrySelected},
					Action{Text: "Remover da lista", OnTriggered: cv.removeSelected},
				},
				Columns: []TableViewColumn{
					{Title: "Arquivo", Width: 380},
					{Title: "Status", Width: 220},
					{Title: "Progresso", Width: 140},
					{Title: "Velocidade", Width: 100},
					{Title: "Restante", Width: 80},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "Cancelar selecionados", OnClicked: cv.cancelSelected},
					PushButton{Text: "Tentar de novo", OnClicked: cv.retrySelected},
					PushButton{Text: "Limpar concluídos", OnClicked: cv.clearDone},
					HSpacer{},
					PushButton{Text: "Abrir arquivo convertido", OnClicked: cv.openSelected},
					PushButton{Text: "Mostrar na pasta", OnClicked: cv.showSelected},
				},
			},
		},
	}

	err := MainWindow{
		AssignTo:    &a.mw,
		Title:       appTitle + versionSuffix() + " — baixe vídeos, lives, reels e áudios",
		MinSize:     Size{Width: 820, Height: 520},
		Size:        Size{Width: 1000, Height: 700},
		Font:        Font{Family: "Segoe UI", PointSize: 10},
		OnDropFiles: dropFiles,
		Layout: VBox{
			Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 10},
			Spacing: 8,
		},
		Children: []Widget{
			TabWidget{
				AssignTo: &tabs,
				Pages:    []TabPage{baixarPage, converterPage},
			},
			Label{AssignTo: &a.statusLB, Text: "Preparando…"},
		},
	}.Create()
	if err != nil {
		return err
	}

	// Trocar de aba reexibe os filhos da página; reaplica o "Mostrar opções".
	applyOpts := func() { optsGB.SetVisible(showOptsCK.Checked()) }
	tabs.CurrentIndexChanged().Attach(applyOpts)
	applyOpts()

	// Fonte usada para desenhar a barra de progresso nas tabelas.
	a.cellFont = dl.tv.Font()

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

		a.ctable.mu.Lock()
		citems := append([]*DlJob(nil), a.ctable.items...)
		a.ctable.mu.Unlock()
		for _, j := range citems {
			a.cancelConv(j)
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
