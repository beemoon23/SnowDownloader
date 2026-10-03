# SnowDownloader

App de Windows (janela própria, sem navegador) para baixar vídeos, lives, reels e áudios de
YouTube, Twitch, Instagram, TikTok, X/Twitter, Facebook, Vimeo, Reddit e mais de mil outros sites.

Por baixo ele usa o [yt-dlp](https://github.com/yt-dlp/yt-dlp) e o ffmpeg. Na **primeira vez**
que abre, o app baixa os dois sozinho para a pasta `tools` ao lado do `.exe`
(se não puder escrever lá, usa `%LOCALAPPDATA%\SnowDownloader\tools`).

## Como usar

1. Cole um ou vários links (um por linha).
2. Escolha qualidade, pasta e opções.
3. Clique em **Baixar**.

| Recurso | Como |
|---|---|
| Só áudio | Qualidade → "Só áudio — MP3" |
| Playlist / canal inteiro | Marque "Baixar playlist / canal inteiro" |
| Live da Twitch / YouTube | Cole o link da live. Marque "gravar desde o início" se a plataforma permitir |
| Instagram, conteúdo com login | Escolha seu navegador em "Cookies do navegador" (fique logado nele) |
| Quebrou depois de um tempo | Botão **Atualizar yt-dlp** |
| Algo avançado | Campo "Argumentos extras" (ex.: `--limit-rate 2M`) |

As opções ficam salvas em `%APPDATA%\SnowDownloader\settings.json`.

## Compilar (pelo GitHub, sem instalar nada)

1. Suba todos os arquivos deste projeto para um repositório (inclusive `.github/workflows/build.yml`).
2. Aba **Actions** → "Build SnowDownloader" → roda sozinho a cada commit na `main`.
3. Quando terminar, abra a execução e baixe o artefato **SnowDownloader** (o `.exe`).
4. Para publicar uma versão fixa: crie uma tag `v1.0.0` — o `.exe` vai para a aba **Releases**.

## Compilar localmente (opcional)

```
go mod tidy
go install github.com/akavel/rsrc@latest
rsrc -manifest app.manifest -o rsrc.syso
go build -ldflags "-H=windowsgui -s -w" -o SnowDownloader.exe .
```

## Limites

- Conteúdo com DRM (Netflix, Disney+, Globoplay etc.) não baixa.
- Instagram/Twitch/YouTube às vezes bloqueiam até o yt-dlp ser atualizado.
- Baixar conteúdo de terceiros pode ir contra os termos da plataforma; use para o que você
  tem direito (uso pessoal, seu próprio conteúdo, material liberado).
- Cancelar uma live no meio deixa o arquivo parcial (ele continua tocável, pois usa MPEG-TS).
