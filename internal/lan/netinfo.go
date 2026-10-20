package lan

// 本机网卡信息探测（客户端 hello 自报 MAC/IP 用）。

import "net"

// firstNIC 返回第一张"像样"网卡的 (MAC, IPv4)：非回环、有 MAC、已启用。
func firstNIC() (mac, ip string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	for _, it := range ifaces {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 || len(it.HardwareAddr) == 0 {
			continue
		}
		addrs, err := it.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				return it.HardwareAddr.String(), ipn.IP.String()
			}
		}
	}
	return "", ""
}

// LocalIPv4s 返回所有非回环 IPv4（管理端"分发"页展示候选地址）。
func LocalIPv4s() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			out = append(out, ipn.IP.String())
		}
	}
	return out
}
