package dockerapi

import (
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

var (
	caddyProxyLabel = regexp.MustCompile(`^(caddy(?:_[0-9]+)?)\.reverse_proxy(?:_[0-9]+)?$`)
	caddyUpstreams  = regexp.MustCompile(`(?:^|\s)\{\{\s*upstreams(?:\s+[^{}]*)?\s*\}\}(?:$|\s)`)
	dockerBridge    = regexp.MustCompile(`^br-[0-9a-f]{12}$`)
)

// caddySites projects the site labels that point to this container. Other
// Caddy directives and arbitrary Docker labels stay out of the inventory.
func caddySites(name string, labels map[string]string) []string {
	var keys []string
	for key, value := range labels {
		match := caddyProxyLabel.FindStringSubmatch(key)
		if match == nil || len(value) > 4096 {
			continue
		}
		// A scoped matcher needs its own URL path or request conditions.
		arguments := strings.TrimSpace(value)
		if strings.HasPrefix(arguments, "/") || strings.HasPrefix(arguments, "@") {
			continue
		}
		if caddyUpstreams.MatchString(arguments) || proxiesToContainer(name, arguments) {
			keys = append(keys, match[1])
		}
	}
	slices.Sort(keys)
	var sites []string
	for _, key := range slices.Compact(keys) {
		value := labels[key]
		if len(value) > 4096 {
			continue
		}
		for _, address := range strings.Fields(strings.ReplaceAll(value, ",", " ")) {
			if len(address) <= 256 && !slices.Contains(sites, address) {
				sites = append(sites, address)
				if len(sites) == 8 {
					return sites
				}
			}
		}
	}
	return sites
}

func proxiesToContainer(name, value string) bool {
	if name == "" {
		return false
	}
	for _, upstream := range strings.Fields(value) {
		upstream = strings.TrimPrefix(strings.TrimPrefix(upstream, "http://"), "https://")
		host, port, err := net.SplitHostPort(upstream)
		if err == nil && port != "" && strings.EqualFold(host, name) {
			return true
		}
	}
	return false
}

// hostAddresses supplies candidates for wildcard Docker port bindings. The
// browser's network determines which of these addresses it can actually reach.
func hostAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var addresses []netip.Addr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Name == "docker0" || iface.Name == "docker_gwbridge" || dockerBridge.MatchString(iface.Name) {
			continue
		}
		interfaceAddrs, _ := iface.Addrs()
		for _, interfaceAddr := range interfaceAddrs {
			prefix, err := netip.ParsePrefix(interfaceAddr.String())
			address := prefix.Addr().Unmap()
			if err == nil && address.IsGlobalUnicast() {
				addresses = append(addresses, address)
			}
		}
	}
	slices.SortFunc(addresses, func(a, b netip.Addr) int {
		// Prefer LAN addresses, then let netip order IPv4 before IPv6.
		if a.IsPrivate() != b.IsPrivate() {
			if a.IsPrivate() {
				return -1
			}
			return 1
		}
		return a.Compare(b)
	})
	var hosts []string
	for _, address := range slices.Compact(addresses) {
		hosts = append(hosts, address.String())
		if len(hosts) == 8 {
			break
		}
	}
	return hosts
}
