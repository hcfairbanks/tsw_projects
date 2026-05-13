package util

import "net"

// GetInternalIP returns the first non-loopback, non-link-local IPv4 address.
func GetInternalIP() string {
	linkLocal := net.IPNet{
		IP:   net.IP{169, 254, 0, 0},
		Mask: net.CIDRMask(16, 32),
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil && !linkLocal.Contains(ipNet.IP) {
				return ipNet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}
