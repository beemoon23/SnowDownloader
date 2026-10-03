package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

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
	a := &SnowApp{
		table:    &JobTable{},
		tools:    newToolPaths(),
		settings: loadSettings(),
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

	addAndStart := func() {
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
			walk.MsgBox(a.mw, appTitle, "Cole pelo menos um link (começando com http:// ou https://).", walk.MsgBoxIconInformation)
			return
		}
		opts := currentOpts()
		if opts.OutDir == "" {
			walk.MsgBox(a.mw, appTitle, "Escolha a pasta de destino.", walk.MsgBoxIconInformation)
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
		a.startQueue()
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

	err := MainWindow{
		AssignTo: &a.mw,
		Title:    appTitle + " — baixe vídeos, lives, reels e áudios",
		MinSize:  Size{Width: 860, Height: 640},
		Size:     Size{Width: 1000, Height: 740},
		Layout:   VBox{},
		Children: []Widget{
			GroupBox{
				Title:  "Links (um por linha) — YouTube, Twitch, Instagram, TikTok, X, Facebook, Vimeo, Reddit…",
				Layout: VBox{},
				Children: []Widget{
					TextEdit{
						AssignTo: &urlEdit,
						VScroll:  true,
						MinSize:  Size{Height: 80},
						MaxSize:  Size{Height: 130},
					},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							PushButton{
								Text: "Colar da área de transferência",
								OnClicked: func() {
									txt, err := walk.Clipboard().Text()
									if err != nil || strings.TrimSpace(txt) == "" {
										return
									}
									cur := strings.TrimRight(urlEdit.Text(), "\r\n ")
									if cur != "" {
										cur += "\r\n"
									}
									urlEdit.SetText(cur + strings.TrimSpace(txt))
								},
							},
							HSpacer{},
							PushButton{
								Text:      "▶  Baixar",
								MinSize:   Size{Width: 140, Height: 32},
								OnClicked: addAndStart,
							},
						},
					},
				},
			},
			GroupBox{
				Title:  "Opções",
				Layout: VBox{},
				Children: []Widget{
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							Label{Text: "Qualidade:"},
							ComboBox{
								AssignTo:     &qualityCB,
								Model:        qualityNames(),
								CurrentIndex: clamp(s.Quality, len(qualityOptions)),
								MinSize:      Size{Width: 280},
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
				AssignTo:         &tv,
				AlternatingRowBG: true,
				MultiSelection:   true,
				ColumnsOrderable: false,
				Model:            a.table,
				Columns: []TableViewColumn{
					{Title: "Título / link", Width: 400},
					{Title: "Status", Width: 230},
					{Title: "Progresso", Width: 80},
					{Title: "Velocidade", Width: 100},
					{Title: "Restante", Width: 80},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{
						Text: "Cancelar selecionados",
						OnClicked: func() {
							for _, j := range selectedJobs() {
								a.cancelJob(j)
							}
						},
					},
					PushButton{
						Text: "Tentar de novo",
						OnClicked: func() {
							sel := selectedJobs()
							a.table.mu.Lock()
							for _, j := range sel {
								if j.State == stError || j.State == stCanceled {
									j.State = stWaiting
									j.canceled = false
									j.Status = "Na fila"
									j.Percent, j.Speed, j.ETA = "", "", ""
								}
							}
							a.table.mu.Unlock()
							a.table.PublishRowsReset()
							a.maxConcurrent = concCB.CurrentIndex() + 1
							a.startQueue()
						},
					},
					PushButton{
						Text: "Limpar concluídos",
						OnClicked: func() {
							a.table.mu.Lock()
							kept := a.table.items[:0]
							for _, j := range a.table.items {
								if j.State == stWaiting || j.State == stRunning {
									kept = append(kept, j)
								}
							}
							a.table.items = kept
							a.table.mu.Unlock()
							a.table.PublishRowsReset()
						},
					},
					HSpacer{},
					PushButton{
						Text: "Abrir pasta",
						OnClicked: func() {
							dir := strings.TrimSpace(outEdit.Text())
							_ = os.MkdirAll(dir, 0o755)
							_ = exec.Command("explorer", dir).Start()
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
		a.setStatus("yt-dlp " + a.ytDlpVersion() + " pronto. Cole os links e clique em Baixar.")
	}()

	// Nesta versão do walk o loop principal roda via walk.App().Run().
	walk.App().Run()
	return nil
}

func clamp(i, n int) int {
	if i < 0 || i >= n {
		return 0
	}
	return i
}
