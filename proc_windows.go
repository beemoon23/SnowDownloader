package main

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow impede que uma janela preta de console pisque a cada processo.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

// killTree mata o processo e todos os filhos (yt-dlp chama ffmpeg).
func killTree(pid int) {
	k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	hideWindow(k)
	_ = k.Run()
}
