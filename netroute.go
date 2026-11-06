package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per-source network routes, chosen inside the app; system settings are never touched.
//
//   - "system" (or ""): the operating system's routing, so a VPN applies.
//   - "direct": connections are bound to the physical network interface (the one whose
//     default route has a gateway), bypassing a VPN's tunnel interface. Name lookups for
//     these connections go out the same way.
//   - "socks5://[user:pass@]host:port" or "http://host:port": through that proxy.

const (
	routeSystem = "system"
	routeDirect = "direct"
)

type Router struct {
	mu         sync.Mutex
	iface      string // configured interface for "direct"; "" = detect
	transports map[string]*http.Transport
}

var router = &Router{transports: map[string]*http.Transport{}}

func normalizeRoute(r string) string {
	r = strings.TrimSpace(r)
	if r == "" {
		return routeSystem
	}
	return r
}

func validRoute(r string) error {
	switch r = normalizeRoute(r); {
	case r == routeSystem, r == routeDirect:
		return nil
	case strings.HasPrefix(r, "socks5://"), strings.HasPrefix(r, "socks5h://"), strings.HasPrefix(r, "http://"):
		u, err := url.Parse(r)
		if err != nil || u.Host == "" {
			return fmt.Errorf("%q is not a proxy URL", r)
		}
		return nil
	}
	return fmt.Errorf("route must be system, direct, or a socks5:// or http:// proxy URL")
}

func (r *Router) SetInterface(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.iface != name {
		r.iface = name
		for _, t := range r.transports {
			t.CloseIdleConnections()
		}
		r.transports = map[string]*http.Transport{}
	}
}

// DirectInterface is the interface "direct" connections use.
func (r *Router) DirectInterface() (string, error) {
	r.mu.Lock()
	name := r.iface
	r.mu.Unlock()
	if name != "" {
		return name, nil
	}
	return detectPhysicalInterface()
}

// Dial connects to addr over route.
func (r *Router) Dial(ctx context.Context, route, network, addr string) (net.Conn, error) {
	route = normalizeRoute(route)
	switch {
	case route == routeSystem:
		d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, network, addr)
	case route == routeDirect:
		iface, err := r.DirectInterface()
		if err != nil {
			return nil, err
		}
		return dialDirect(ctx, iface, network, addr)
	case strings.HasPrefix(route, "socks5"):
		return dialSOCKS5(ctx, route, addr)
	case strings.HasPrefix(route, "http://"):
		return dialHTTPProxy(ctx, route, addr)
	}
	return nil, fmt.Errorf("unknown route %q", route)
}

// Transport is a shared HTTP transport for route.
func (r *Router) Transport(route string) *http.Transport {
	route = normalizeRoute(route)
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.transports[route]; t != nil {
		return t
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.ResponseHeaderTimeout = 60 * time.Second
	t.MaxIdleConnsPerHost = 8
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return r.Dial(ctx, route, network, addr)
	}
	r.transports[route] = t
	return t
}

func describeRoute(route string) string {
	route = normalizeRoute(route)
	switch route {
	case routeSystem:
		return "system (VPN applies)"
	case routeDirect:
		if iface, err := router.DirectInterface(); err == nil {
			return "direct via " + iface
		}
		return "direct (no physical interface found)"
	}
	if u, err := url.Parse(route); err == nil {
		u.User = nil
		return "proxy " + u.String()
	}
	return route
}

// --- direct ---------------------------------------------------------------------------------

func dialDirect(ctx context.Context, iface, network, addr string) (net.Conn, error) {
	d := directDialer(iface)
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("direct connection via %s: %w", iface, err)
	}
	return c, nil
}

func directDialer(iface string) *net.Dialer {
	base := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	bindToInterface(base, iface)
	plain := &net.Dialer{Timeout: 10 * time.Second}
	base.Resolver = &net.Resolver{
		PreferGo: true,
		// Name lookups leave through the same interface, unless the name server is local
		// (systemd-resolved and the like).
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if host, _, err := net.SplitHostPort(address); err == nil {
				if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
					return plain.DialContext(ctx, network, address)
				}
			}
			d := &net.Dialer{Timeout: 10 * time.Second}
			bindToInterface(d, iface)
			return d.DialContext(ctx, network, address)
		},
	}
	return base
}

var tunnelNames = []string{"tun", "tap", "wg", "utun", "ppp", "happ", "xray", "sing", "clash", "nekoray", "tailscale",
	"zt", "nordlynx", "proton", "mullvad", "outline", "amnezia", "awg", "vpn", "docker", "veth", "br-", "virbr"}

func looksLikeTunnel(name string) bool {
	n := strings.ToLower(name)
	for _, p := range tunnelNames {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// --- proxies --------------------------------------------------------------------------------

func dialSOCKS5(ctx context.Context, proxyURL, addr string) (net.Conn, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 30 * time.Second}
	c, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 proxy %s: %w", u.Host, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	} else {
		c.SetDeadline(time.Now().Add(30 * time.Second))
	}
	fail := func(format string, args ...any) (net.Conn, error) {
		c.Close()
		return nil, fmt.Errorf("SOCKS5 proxy %s: %s", u.Host, fmt.Sprintf(format, args...))
	}
	methods := []byte{0}
	if u.User != nil {
		methods = []byte{2}
	}
	c.Write(append([]byte{5, byte(len(methods))}, methods...))
	var resp [2]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil || resp[0] != 5 {
		return fail("bad handshake")
	}
	if resp[1] == 2 {
		user := u.User.Username()
		pass, _ := u.User.Password()
		msg := append([]byte{1, byte(len(user))}, user...)
		msg = append(append(msg, byte(len(pass))), pass...)
		c.Write(msg)
		if _, err := io.ReadFull(c, resp[:]); err != nil || resp[1] != 0 {
			return fail("login refused")
		}
	} else if resp[1] != 0 {
		return fail("no acceptable authentication method")
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fail("%v", err)
	}
	port, _ := strconv.Atoi(portText)
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(append(req, 1), ip.To4()...)
	} else if ip != nil {
		req = append(append(req, 4), ip.To16()...)
	} else {
		req = append(append(req, 3, byte(len(host))), host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	c.Write(req)
	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return fail("%v", err)
	}
	if h[1] != 0 {
		return fail("connection to %s refused (code %d)", addr, h[1])
	}
	skip := 0
	switch h[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		var l [1]byte
		io.ReadFull(c, l[:])
		skip = int(l[0])
	}
	io.CopyN(io.Discard, c, int64(skip+2))
	c.SetDeadline(time.Time{})
	return c, nil
}

func dialHTTPProxy(ctx context.Context, proxyURL, addr string) (net.Conn, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 30 * time.Second}
	c, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("HTTP proxy %s: %w", u.Host, err)
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))
	req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
	if u.User != nil {
		pass, _ := u.User.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pass)) + "\r\n"
	}
	c.Write([]byte(req + "\r\n"))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil || resp.StatusCode != 200 {
		c.Close()
		if err == nil {
			err = errors.New(resp.Status)
		}
		return nil, fmt.Errorf("HTTP proxy %s: %w", u.Host, err)
	}
	c.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
