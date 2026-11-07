package core

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Asking the router to forward the Soulseek port (UPnP IGD), as Nicotine+ does by default:
// with the port open, users behind closed ports can connect to us too. Only the router's
// port mapping is changed, for a limited time, renewed while the app runs and removed when
// it quits.

type upnpMapping struct {
	client      *http.Client
	control     string
	service     string
	port        int
	externalIP  string
	internalIP  string
	description string
}

var upnpServices = []string{
	"urn:schemas-upnp-org:service:WANIPConnection:2",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// upnpLAN returns the physical interface and its IPv4 address (the router is found there,
// not through a VPN's tunnel).
func upnpLAN() (string, net.IP, error) {
	iface, err := router.DirectInterface()
	if err != nil {
		return "", nil, err
	}
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return "", nil, err
	}
	addrs, _ := ifi.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return iface, ipn.IP.To4(), nil
		}
	}
	return "", nil, fmt.Errorf("%s has no IPv4 address", iface)
}

// upnpDiscover finds the router's port-mapping service.
func upnpDiscover(ctx context.Context) (*upnpMapping, error) {
	iface, ip, err := upnpLAN()
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{}
	bindToInterface(d, iface)
	lc := net.ListenConfig{Control: d.Control}
	pc, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for _, st := range []string{"urn:schemas-upnp-org:device:InternetGatewayDevice:1", "urn:schemas-upnp-org:device:InternetGatewayDevice:2",
		"urn:schemas-upnp-org:service:WANIPConnection:1"} {
		msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + st + "\r\n\r\n"
		pc.WriteTo([]byte(msg), dst)
	}
	hd := &net.Dialer{Timeout: 5 * time.Second}
	bindToInterface(hd, iface)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: hd.DialContext}}
	seen := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	buf := make([]byte, 4096)
	var lastErr error
	for time.Now().Before(deadline) && ctx.Err() == nil {
		pc.SetReadDeadline(deadline)
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		loc := ""
		for _, line := range strings.Split(string(buf[:n]), "\r\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "location") {
				loc = strings.TrimSpace(v)
			}
		}
		if loc == "" || seen[loc] {
			continue
		}
		seen[loc] = true
		m, err := upnpDescribe(ctx, client, loc)
		if err != nil {
			lastErr = err
			continue
		}
		m.internalIP = ip.String()
		return m, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no UPnP router answered (UPnP may be off in the router)")
}

// upnpDescribe reads a device description for the port-mapping service.
func upnpDescribe(ctx context.Context, client *http.Client, loc string) (*upnpMapping, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", loc, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var desc struct {
		URLBase string `xml:"URLBase"`
	}
	xml.Unmarshal(data, &desc)
	type service struct {
		Type    string `xml:"serviceType"`
		Control string `xml:"controlURL"`
	}
	var services []service
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "service" {
			var s service
			if dec.DecodeElement(&s, &se) == nil {
				services = append(services, s)
			}
		}
	}
	base, err := url.Parse(FirstNonEmpty(strings.TrimSpace(desc.URLBase), loc))
	if err != nil {
		return nil, err
	}
	for _, want := range upnpServices {
		for _, s := range services {
			if strings.TrimSpace(s.Type) == want {
				ctl, err := base.Parse(strings.TrimSpace(s.Control))
				if err != nil {
					continue
				}
				return &upnpMapping{client: client, control: ctl.String(), service: want}, nil
			}
		}
	}
	return nil, fmt.Errorf("the router at %s offers no port mapping", base.Host)
}

func (m *upnpMapping) soap(ctx context.Context, action, args string) ([]byte, error) {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:` + action + ` xmlns:u="` + m.service + `">` +
		args + `</u:` + action + `></s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, "POST", m.control, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+m.service+"#"+action+`"`)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		desc := ""
		if i, j := bytes.Index(data, []byte("<errorDescription>")), bytes.Index(data, []byte("</errorDescription>")); i >= 0 && j > i {
			desc = string(data[i+18 : j])
		}
		return nil, fmt.Errorf("the router refused %s (HTTP %d %s)", action, resp.StatusCode, desc)
	}
	return data, nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// upnpMap forwards TCP port on the router to this computer for lease.
func upnpMap(ctx context.Context, port int, description string, lease time.Duration) (*upnpMapping, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, err := upnpDiscover(ctx)
	if err != nil {
		return nil, err
	}
	m.port, m.description = port, description
	if data, err := m.soap(ctx, "GetExternalIPAddress", ""); err == nil {
		if i, j := bytes.Index(data, []byte("<NewExternalIPAddress>")), bytes.Index(data, []byte("</NewExternalIPAddress>")); i >= 0 && j > i {
			m.externalIP = string(data[i+22 : j])
		}
	}
	args := fmt.Sprintf("<NewRemoteHost></NewRemoteHost><NewExternalPort>%d</NewExternalPort><NewProtocol>TCP</NewProtocol>"+
		"<NewInternalPort>%d</NewInternalPort><NewInternalClient>%s</NewInternalClient><NewEnabled>1</NewEnabled>"+
		"<NewPortMappingDescription>%s</NewPortMappingDescription><NewLeaseDuration>%d</NewLeaseDuration>",
		port, port, m.internalIP, xmlEscape(description), int(lease.Seconds()))
	if _, err := m.soap(ctx, "AddPortMapping", args); err != nil {
		// Some routers only take permanent mappings.
		if _, err2 := m.soap(ctx, "AddPortMapping", strings.Replace(args, fmt.Sprintf("<NewLeaseDuration>%d<", int(lease.Seconds())), "<NewLeaseDuration>0<", 1)); err2 != nil {
			return nil, err
		}
	}
	return m, nil
}

// remove takes the port mapping away again.
func (m *upnpMapping) remove() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.soap(ctx, "DeletePortMapping", fmt.Sprintf("<NewRemoteHost></NewRemoteHost><NewExternalPort>%d</NewExternalPort><NewProtocol>TCP</NewProtocol>", m.port))
}

// checkSoulseekPort asks the Soulseek site's port checker whether port can be reached from
// the internet at the address our connections come from (over route).
func checkSoulseekPort(ctx context.Context, route string, port int) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://www.slsknet.org/porttest.php?port=%d", port), nil)
	if err != nil {
		return false, err
	}
	resp, err := (&http.Client{Transport: router.Transport(route)}).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	low := strings.ToLower(string(data))
	switch {
	case strings.Contains(low, fmt.Sprintf("%d/tcp open", port)):
		return true, nil
	case strings.Contains(low, fmt.Sprintf("%d/tcp closed", port)), strings.Contains(low, fmt.Sprintf("%d/tcp filtered", port)):
		return false, nil
	}
	return false, errors.New("the port checker gave an answer this app doesn't understand")
}
