//go:build windows

package lan

import (
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

func setBroadcastOpt(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
}

// setMulticastOutOpt 指定组播出站网卡 IP 并把 TTL 压到 1（只在局域网内传播），
// 同时保证本机回环（教师机上的演示客户端也能收到）。
func setMulticastOutOpt(fd uintptr, ifIP net.IP) error {
	h := syscall.Handle(fd)
	if err := syscall.SetsockoptInt(h, syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1); err != nil {
		return err
	}
	if err := syscall.SetsockoptInt(h, syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 1); err != nil {
		return err
	}
	if ip4 := ifIP.To4(); ip4 != nil {
		var addr [4]byte
		copy(addr[:], ip4)
		if err := windows.SetsockoptInet4Addr(windows.Handle(fd), windows.IPPROTO_IP, windows.IP_MULTICAST_IF, addr); err != nil {
			return err
		}
	}
	return nil
}

// setUDPBufOpt 调大组播 socket 收发缓冲（Windows 上返回 int 的
// syscall.SetsockoptInt 走 Handle 参数）。失败不致命。
func setUDPBufOpt(fd uintptr, send bool, size int) {
	opt := syscall.SO_RCVBUF
	if send {
		opt = syscall.SO_SNDBUF
	}
	_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, opt, size)
}
