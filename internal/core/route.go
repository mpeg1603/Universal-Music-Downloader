package core

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// RouteSpec is a route taken apart, for editing: system, direct, or a proxy (type, address,
// login). Settings keep routes as text ("system", "direct", "socks5://user:pass@host:port").
type RouteSpec struct {
	Kind string // one of RouteKinds
	Host string
	Port int
	User string
	Pass string
}

// RouteKinds are the kinds of route, in the order a frontend offers them.
var RouteKinds = []string{RouteSystem, RouteDirect, "socks5", "http"}

// RouteKindLabels describe them.
var RouteKindLabels = map[string]string{
	RouteSystem: "system (the operating system's routing; your VPN applies)",
	RouteDirect: "direct (your physical network interface, bypassing a VPN)",
	"socks5":    "SOCKS5 proxy",
	"http":      "HTTP proxy",
}

// ParseRoute takes a route apart; text that isn't a valid route gives the system route.
func ParseRoute(s string) RouteSpec {
	s = NormalizeRoute(s)
	switch s {
	case RouteSystem, RouteDirect:
		return RouteSpec{Kind: s}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return RouteSpec{Kind: RouteSystem}
	}
	r := RouteSpec{Kind: strings.TrimSuffix(u.Scheme, "h"), Host: u.Hostname()}
	if r.Kind != "socks5" && r.Kind != "http" {
		return RouteSpec{Kind: RouteSystem}
	}
	r.Port, _ = strconv.Atoi(u.Port())
	if u.User != nil {
		r.User = u.User.Username()
		r.Pass, _ = u.User.Password()
	}
	return r
}

// String puts a route back together as settings keep it.
func (r RouteSpec) String() string {
	switch r.Kind {
	case RouteSystem, RouteDirect, "":
		return FirstNonEmpty(r.Kind, RouteSystem)
	}
	u := url.URL{Scheme: r.Kind, Host: net.JoinHostPort(r.Host, strconv.Itoa(r.Port))}
	if r.User != "" || r.Pass != "" {
		u.User = url.UserPassword(r.User, r.Pass)
	}
	return u.String()
}

func (r RouteSpec) Validate() error {
	switch r.Kind {
	case RouteSystem, RouteDirect:
		return nil
	case "socks5", "http":
		if strings.TrimSpace(r.Host) == "" {
			return fmt.Errorf("the proxy's address is missing")
		}
		if strings.ContainsAny(r.Host, " /@") {
			return fmt.Errorf("%q isn't a host name or IP address", r.Host)
		}
		if r.Port < 1 || r.Port > 65535 {
			return fmt.Errorf("the proxy's port must be between 1 and 65535")
		}
		return nil
	}
	return fmt.Errorf("unknown route type %q", r.Kind)
}

// Describe is a short description for lists, without the password.
func (r RouteSpec) Describe() string {
	switch r.Kind {
	case RouteSystem, RouteDirect:
		return DescribeRoute(r.Kind)
	}
	s := r.Kind + " proxy " + net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
	if r.User != "" {
		s += " as " + r.User
	}
	return s
}

// Short is a brief label for tables: the kind, and a proxy's address (never the password).
func (r RouteSpec) Short() string {
	switch r.Kind {
	case RouteSystem, RouteDirect:
		return r.Kind
	}
	return r.Kind + " " + net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
}
