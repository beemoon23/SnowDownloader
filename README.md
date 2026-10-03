# SnowDownloader

App de Windows (janela própria, sem navegador) para baixar vídeos, lives, reels e áudios de
YouTube, Twitch, Instagram, TikTok, X/Twitter, Facebook, Vimeo, Reddit e mais de mil outros sites.

Por baixo ele usa o [yt-dlp](https://github.com/yt-dlp/yt-dlp) e o ffmpeg. Na **primeira vez**
que abre, o app baixa os dois sozinho para a pasta `tools` ao lado do `.exe`
(se não puder escrever lá, usa `%LOCALAPPDATA%\SnowDownloader\tools`).

## Baixar o programa

Os links abaixo sempre apontam para a versão mais recente:

- **Instalador (recomendado):** [SnowDownloader-Setup.exe](https://github.com/beemoon23/SnowDownloader/releases/latest/download/SnowDownloader-Setup.exe)
  — instala só para o seu usuário (não pede administrador) e cria atalho no menu Iniciar.
- **Portátil:** [SnowDownloader.exe](https://github.com/beemoon23/SnowDownloader/releases/latest/download/SnowDownloader.exe)
  — é só abrir, sem instalar.

Como o programa não tem assinatura digital, o Windows pode mostrar "O Windows protegeu o
computador" na primeira vez. Clique em **Mais informações → Executar assim mesmo**.

### Atualização automática

Ao abrir, o app confere se há versão nova no GitHub. Se houver, pergunta "Atualizar agora?",
baixa, troca o próprio `.exe` e reabre sozinho. Também dá para forçar pelo botão
**Atualizar app**. O título da janela mostra o código da versão (ex.: `SnowDownloader 7e80994`).

A atualização não acontece com download em andamento. O yt-dlp também se atualiza sozinho,
no máximo uma vez por semana.

> Cópias compiladas fora do GitHub não têm código de versão e não se atualizam sozinhas.
> Baixe uma vez pelos links acima e dali em diante é automático.

## Como usar

1. **Cole o link** (Ctrl+V na caixa ou botão **Colar e baixar**). O download começa sozinho.
2. Ou arraste um arquivo `.txt` (um link por linha) para a janela.
3. Acompanhe na lista. Duplo clique num item concluído abre o vídeo.

Clique com o botão direito num item para: abrir o arquivo, mostrar na pasta, copiar o link,
cancelar, tentar de novo ou remover da lista.

| Recurso | Como |
|---|---|
| Vários links de uma vez | Cole um por linha, ou arraste um `.txt` para a janela |
| Só áudio | Mostrar opções → Qualidade → "Só áudio — MP3" |
| Playlist / canal inteiro | Mostrar opções → "Baixar playlist / canal inteiro" |
| Só um trecho do vídeo | Mostrar opções → "Baixar só um trecho" → de / até (ex.: `1:30` e `5:00`; vazio no "até" = até o fim) |
| Live da Twitch / YouTube | Cole o link da live. Marque "gravar desde o início" se a plataforma permitir |
| Instagram, conteúdo com login | Escolha seu navegador em "Cookies do navegador" (fique logado nele) |
| Link já baixado antes | O app avisa e pergunta se quer baixar de novo (Mostrar opções → "Limpar histórico" apaga essa memória) |
| Deixar rodando em segundo plano | Minimize: a janela vai para a bandeja, perto do relógio. Clique no ícone para abrir |
| Aviso quando termina | Balão do Windows ao concluir ou falhar |
| Quebrou depois de um tempo | Botão **Atualizar yt-dlp** |
| Algo avançado | Campo "Argumentos extras" (ex.: `--limit-rate 2M`) |

Observações:

- O corte de trecho usa os pontos de quadro-chave do vídeo, então pode começar ou terminar
  alguns segundos diferente do digitado. Vale para os próximos links colados.
- Links repetidos que já estão na lista são ignorados.
- As opções ficam salvas em `%APPDATA%\SnowDownloader\settings.json` e o histórico em
  `%APPDATA%\SnowDownloader\history.json`.

## Como o GitHub compila e publica

A cada commit na `main`, o workflow **Build SnowDownloader** (aba **Actions**):

1. compila o `SnowDownloader.exe`, com o ícone e o código da versão embutidos;
2. gera o instalador `SnowDownloader-Setup.exe` (Inno Setup, script em `installer.iss`);
3. recria a release **latest** (aba **Releases**) com os dois arquivos. É dela que vêm os links
   fixos acima e é nela que o app confere se há versão nova.

Os arquivos também ficam em **Artifacts**, no fim da página de cada execução.

Para o ícone entrar no `.exe`, o arquivo `snow.ico` precisa estar na raiz do repositório.

## Estrutura do projeto

| Arquivo | O que faz |
|---|---|
| `main.go` | Janela, botões, fila de links, arrastar arquivo |
| `engine.go` | Monta os comandos do yt-dlp, fila de downloads, progresso |
| `model.go` | Dados da tabela de downloads |
| `uiextras.go` | Barra de progresso, cores do status, ícone, balões |
| `tray.go` | Ícone da bandeja e "minimizar para a bandeja" |
| `update.go` | Atualização automática do próprio app e do yt-dlp |
| `history.go` | Histórico anti-duplicado |
| `section.go` | Links dentro de textos e validação do trecho |
| `tools.go` | Baixa yt-dlp e ffmpeg na primeira vez |
| `settings.go` | Opções salvas entre aberturas |
| `shell_windows.go`, `proc_windows.go` | Abrir arquivos/pastas e encerrar processos no Windows |
| `installer.iss` | Script do instalador |
| `.github/workflows/build.yml` | Compilação e publicação automáticas |

## Compilar localmente (opcional)

```
go mod tidy
go install github.com/akavel/rsrc@latest
rsrc -manifest app.manifest -ico snow.ico -o rsrc.syso
go build -ldflags "-H=windowsgui -s -w" -o SnowDownloader.exe .
```

Compilada assim, a cópia fica sem código de versão e não se atualiza sozinha.

## Limites

- Conteúdo com DRM (Netflix, Disney+, Globoplay etc.) não baixa.
- Instagram/Twitch/YouTube às vezes bloqueiam até o yt-dlp ser atualizado.
- Baixar conteúdo de terceiros pode ir contra os termos da plataforma; use para o que você
  tem direito (uso pessoal, seu próprio conteúdo, material liberado).
- Cancelar uma live no meio deixa o arquivo parcial (ele continua tocável, pois usa MPEG-TS).
