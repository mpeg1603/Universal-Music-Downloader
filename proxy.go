package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A proxy on 127.0.0.1 for yt-dlp, so that its downloads take the route chosen for their
// source and count against the bandwidth cap. The route travels as the proxy user name; the
// password is a random secret, so other programs on the machine can't use the proxy.

type LocalProxy struct {
	ln     net.Listener
	secret string
	wg     sync.WaitGroup
}

func startLocalProxy() (*LocalProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	key := make([]byte, 12)
	rand.Read(key)
	p := &LocalProxy{ln: ln, secret: hex.EncodeToString(key)}
	go p.serve()
	return p, nil
}

func (p *LocalProxy) Close() { p.ln.Close() }

// URL is the proxy address to give yt-dlp for route.
func (p *LocalProxy) URL(route string) string {
	if p == nil {
		return "" // not started: yt-dlp goes the system's way
	}
	return "http://" + hex.EncodeToString([]byte(normalizeRoute(route))) + ":" + p.secret + "@" + p.ln.Addr().String()
}

func (p *LocalProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func (p *LocalProxy) route(req *http.Request) (string, bool) {
	auth := req.Header.Get("Proxy-Authorization")
	raw, ok := strings.CutPrefix(auth, "Basic ")
	if !ok {
		return "", false
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", false
	}
	user, pass, _ := strings.Cut(string(data), ":")
	route, err := hex.DecodeString(user)
	if err != nil || pass != p.secret {
		return "", false
	}
	return string(route), true
}

func (p *LocalProxy) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	for {
		c.SetReadDeadline(time.Now().Add(2 * time.Minute))
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		c.SetReadDeadline(time.Time{})
		route, ok := p.route(req)
		if !ok {
			io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"spotify-downloader\"\r\nContent-Length: 0\r\n\r\n")
			return
		}
		if req.Method == http.MethodConnect {
			p.tunnel(c, br, route, req.Host)
			return
		}
		if !p.forward(c, req, route) {
			return
		}
	}
}

func (p *LocalProxy) tunnel(c net.Conn, br *bufio.Reader, route, host string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	up, err := router.Dial(ctx, route, "tcp", host)
	cancel()
	if err != nil {
		debugf("proxy: %s via %s: %v", host, route, err)
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	done := make(chan struct{})
	go func() {
		io.Copy(up, br) // includes anything already buffered
		if tc, ok := up.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		close(done)
	}()
	io.Copy(c, bw.Reader(context.Background(), up))
	c.Close()
	<-done
}

func (p *LocalProxy) forward(c net.Conn, req *http.Request, route string) bool {
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.RequestURI = ""
	resp, err := router.Transport(route).RoundTrip(req)
	if err != nil {
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return false
	}
	body := resp.Body
	defer body.Close()
	resp.Body = io.NopCloser(bw.Reader(context.Background(), body))
	return resp.Write(c) == nil && !resp.Close
}
