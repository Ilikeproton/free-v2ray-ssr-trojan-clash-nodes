//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32MessageBoxDLL = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW     = user32MessageBoxDLL.NewProc("MessageBoxW")
)

const (
	mbIconError = 0x00000010
	mbTopMost   = 0x00040000
)

func showStartupError(message string) {
	msgPtr, _ := syscall.UTF16PtrFromString(message)
	titlePtr, _ := syscall.UTF16PtrFromString("Daxionglink 启动失败")
	_, _, _ = procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(msgPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		mbIconError|mbTopMost,
	)
}
