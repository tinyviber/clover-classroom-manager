//go:build windows

package main

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var createMutexW = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateMutexW")

// singleInstance 用命名互斥体保证单实例。用 Global\ 域：HKLM Run 键
// 在每个登录会话都会拉起 agent，Local\ 只管单会话会让两个会话的
// 同名 agent 互踢上线。互斥体句柄随进程生命周期留存（刻意不关）。
func singleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Global\CloverLabAgent`)
	_, _, err := createMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	return !errors.Is(err, syscall.ERROR_ALREADY_EXISTS)
}
