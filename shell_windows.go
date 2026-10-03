package main

import "os/exec"

// openFile abre o arquivo no programa padrão do Windows (player de vídeo etc.).
func openFile(path string) {
	_ = exec.Command("explorer", path).Start()
}

// showInFolder abre o Explorer já com o arquivo selecionado.
func showInFolder(path string) {
	_ = exec.Command("explorer", "/select,", path).Start()
}

// openFolder abre uma pasta no Explorer.
func openFolder(dir string) {
	_ = exec.Command("explorer", dir).Start()
}
