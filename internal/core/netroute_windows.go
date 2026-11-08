//go:build windows

package core

import (
	"fmt"
	"net"
	"syscall"
)

// ipUnicastIf is IP_UNICAST_IF: send through this interface whatever the routing table says
// (the Windows counterpart of Linux's SO_BINDTODEVICE; no administrator rights needed).
const ipUnicastIf = 31

// bindToInterface makes the dialer's sockets use iface (its name as Windows shows it:
// "Ethernet", "Wi-Fi"), and its IPv4 address as the source.
func bindToInterface(d *net.Dialer, iface string) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return
	}
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			d.LocalAddr = &net.TCPAddr{IP: ipn.IP}
			break
		}
	}
	index := ifi.Index
	d.Control = func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			// IPv4 takes the index in network byte order.
			be := int(uint32(index)>>24 | uint32(index)>>8&0xff00 | uint32(index)<<8&0xff0000 | uint32(index)<<24)
			serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, ipUnicastIf, be)
		})
		if err != nil {
			return err
		}
		if serr != nil {
			return fmt.Errorf("binding to %s: %w", iface, serr)
		}
		return nil
	}
}

// DetectPhysicalInterface finds an interface that's up, has a private IPv4 address and
// isn't a VPN's tunnel.
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
