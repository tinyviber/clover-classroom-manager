//go:build !windows

package lan

import (
	"net"
	"syscall"
)

func setBroadcastOpt(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
}

// setMulticastOutOpt 指定组播出站网卡 IP 并把 TTL 压到 1（只在局域网内传播），
// 同时保证本机回环（教师机上的演示客户端也能收到）。
func setMulticastOutOpt(fd uintptr, ifIP net.IP) error {
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1); err != nil {
		return err
	}
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 1); err != nil {
		return err
	}
	if ip4 := ifIP.To4(); ip4 != nil {
		var addr [4]byte
		copy(addr[:], ip4)
		if err := syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, addr); err != nil {
			return err
		}
	}
	return nil
}

// setUDPBufOpt 调大组播 socket 收发缓冲：单帧 ~1950 个分片连发时
// 默认缓冲（~212K）会 ENOBUFS/内核丢包。失败不致命（尽力而为）。
func setUDPBufOpt(fd uintptr, send bool, size int) {
	opt := syscall.SO_RCVBUF
	if send {
		opt = syscall.SO_SNDBUF
	}
	_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, opt, size)
}
