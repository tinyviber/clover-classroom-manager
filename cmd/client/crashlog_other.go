//go:build !windows

package main

import "os"

// 非 Windows 平台 stderr 有去处（开发期跑在终端），无需接管。
func captureStderr(_ *os.File) {}
