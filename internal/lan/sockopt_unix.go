//go:build !windows

package lan

import "syscall"

func setBroadcastOpt(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
}
