//go:build !linux

package core

import (
	"fmt"
	"net"
)

// bindToInterface binds the dialer to iface's IPv4 address. On systems that route by source
// address this keeps the connection off a VPN tunnel; elsewhere it's best effort.
func bindToInterface(d *net.Dialer, iface string) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return
	}
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			d.LocalAddr = &net.TCPAddr{IP: ipn.IP}
			return
		}
	}
}

func DetectPhysicalInterface() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 || looksLikeTunnel(ifi.Name) {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
				return ifi.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no physical network interface found; set it in Settings → Direct interface")
}
