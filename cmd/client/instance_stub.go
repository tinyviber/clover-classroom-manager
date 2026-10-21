//go:build !windows

package main

// singleInstance 非 Windows（demo 客户端）不做单实例限制。
func singleInstance() bool { return true }
