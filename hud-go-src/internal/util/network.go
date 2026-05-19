package util

import "net"

// GetInternalIP returns the IPv4 address of the interface the OS would use
// to reach the public internet — typically the LAN (or active VPN) adapter.
// Falls back to enumerating interfaces if no default route is available.
func GetInternalIP() string {
	// UDP "dial" doesn't transmit — it just resolves the default route
	// and binds a local address on the interface that would carry the packet.
	// This avoids picking virtual adapters (WSL/Hyper-V/Docker) that have no
	// default gateway.
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer conn.Close()
		if udp, ok := conn.LocalAddr().(*net.UDPAddr); ok && udp.IP.To4() != nil {
			return udp.IP.String()
		}
	}
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
