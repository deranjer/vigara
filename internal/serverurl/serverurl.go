// Package serverurl holds the transport policy shared by the Vigira server and
// agent: which server URLs an agent may use, and when plain HTTP is acceptable.
package serverurl

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

var cgnat = netip.MustParsePrefix("100.64.0.0/10") // includes Tailscale tailnet addresses

// IsLoopbackHost reports whether host is localhost or a loopback IP.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsPrivateNetworkHost reports whether host is addressed within a private or
// overlay network: loopback, RFC 1918, IPv6 unique-local, link-local, the CGNAT
// range used by Tailscale, mDNS ".local" names, ".ts.net" MagicDNS names, and
// single-label names such as "nas". Public IPs and dotted public hostnames are
// never private.
func IsPrivateNetworkHost(host string) bool {
	if IsLoopbackHost(host) {
		return true
	}
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		addr = addr.Unmap()
		return addr.IsPrivate() || addr.IsLinkLocalUnicast() || cgnat.Contains(addr)
	}
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	return name != "" && (!strings.Contains(name, ".") || strings.HasSuffix(name, ".local") || strings.HasSuffix(name, ".ts.net"))
}

// Validate checks that raw is an origin an agent may be pointed at. HTTPS is
// always allowed. HTTP is allowed for loopback hosts, and for other private-network
// hosts only when allowInsecureHTTP is set (the operator's explicit opt-in).
func Validate(raw string, allowInsecureHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("server URL must be an origin without user info, query, or fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if IsLoopbackHost(host) || allowInsecureHTTP && IsPrivateNetworkHost(host) {
			return nil
		}
		if allowInsecureHTTP {
			return errors.New("plain HTTP is only allowed for private-network or Tailscale addresses; use HTTPS for this host")
		}
		return errors.New("server URL must use HTTPS")
	}
	return errors.New("server URL must use HTTPS")
}

// IsSecure reports whether raw is HTTPS or loopback HTTP, i.e. needs no warning.
func IsSecure(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http" && IsLoopbackHost(u.Hostname()))
}
