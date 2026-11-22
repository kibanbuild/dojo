package server

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// ParseTrustedHops validates TRUSTED_PROXY_HOPS: the proxies in front of the API (design 3.1).
// 0 is refused: every environment has at least the edge, and 0 would trust the wrong address.
func ParseTrustedHops(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 8 {
		return 0, fmt.Errorf("TRUSTED_PROXY_HOPS must be a number from 1 to 8, got %q", s)
	}
	return n, nil
}

// clientKey is the rate-limit key of a request. It always returns a key: an entry that cannot be
// parsed falls back to the connection address, which is a shared bucket, never a free pass.
func clientKey(r *http.Request, hops int) string {
	var list []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(v, ",") {
			list = append(list, strings.TrimSpace(e))
		}
	}
	if len(list) >= hops {
		if a, ok := parseAddr(list[len(list)-hops]); ok {
			return bucketOf(a)
		}
	}
	if a, ok := parseAddr(r.RemoteAddr); ok {
		return bucketOf(a)
	}
	return "unknown"
}

func parseAddr(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a, true
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	return a, err == nil
}

// bucketOf maps an address to its key: IPv4 as is, IPv6 by its /64 (one subscriber owns a /64).
func bucketOf(a netip.Addr) string {
	a = a.Unmap().WithZone("")
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}
