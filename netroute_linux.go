//go:build linux

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// bindToInterface makes the dialer's sockets use iface whatever the routing table says
// (SO_BINDTODEVICE; no special privileges are needed for this since Linux 5.7).
func bindToInterface(d *net.Dialer, iface string) {
	d.Control = func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
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

// detectPhysicalInterface finds the interface of the default route that goes through a
// gateway: a VPN's tunnel route usually has none ("default dev tun0").
func detectPhysicalInterface() (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", fmt.Errorf("can't read the routing table: %w", err)
	}
	defer f.Close()
	best, bestMetric := "", -1
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" || fields[2] == "00000000" {
			continue
		}
		if looksLikeTunnel(fields[0]) {
			continue
		}
		metric, _ := strconv.Atoi(fields[6])
		if best == "" || metric < bestMetric {
			best, bestMetric = fields[0], metric
		}
	}
	if best == "" {
		return "", fmt.Errorf("no physical network interface with a default gateway found; set it in Settings → Direct interface")
	}
	return best, nil
}
