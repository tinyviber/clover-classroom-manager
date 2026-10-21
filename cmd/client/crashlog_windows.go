//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// captureStderr 把进程 stderr 重定向进 agent.log：windowsgui 程序没有
// 控制台，runtime panic 默认打到无处去的 fd2——接管后任何 goroutine 的
// panic 都会在日志里留完整堆栈。
func captureStderr(f *os.File) {
	if f == nil {
		return
	}
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
}
