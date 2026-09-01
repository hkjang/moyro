package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

func peerAddress(r *http.Request) (netip.Addr, bool) {
	raw := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	addr, err := netip.ParseAddr(raw)
	return addr.Unmap(), err == nil
}

func (h *handlers) trustedProxyPrefixes() []netip.Prefix {
	if h == nil || h.native == nil {
		return nil
	}
	raw := h.native.currentSiteSettings().TrustedProxyCIDRs
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

func addressTrusted(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func forwardedIP(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(strings.Trim(value, `"`))
	if value == "" || strings.EqualFold(value, "unknown") || strings.HasPrefix(value, "_") {
		return netip.Addr{}, false
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.Trim(value, "[]")
	}
	addr, err := netip.ParseAddr(value)
	return addr.Unmap(), err == nil
}

func forwardedForChain(r *http.Request) ([]netip.Addr, bool) {
	if header := strings.TrimSpace(r.Header.Get("Forwarded")); header != "" {
		parts := strings.Split(header, ",")
		chain := make([]netip.Addr, 0, len(parts))
		for _, element := range parts {
			var found string
			for _, parameter := range strings.Split(element, ";") {
				key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
				if ok && strings.EqualFold(key, "for") {
					found = value
					break
				}
			}
			addr, ok := forwardedIP(found)
			if !ok {
				return nil, false
			}
			chain = append(chain, addr)
		}
		return chain, len(chain) > 0
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	chain := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		addr, ok := forwardedIP(part)
		if !ok {
			return nil, false
		}
		chain = append(chain, addr)
	}
	return chain, len(chain) > 0
}

// clientIP walks the proxy chain from the trusted TCP peer toward the client
// and stops at the first untrusted hop. Forwarded headers are ignored unless
// the immediate peer is in the administrator-managed CIDR allowlist.
func (h *handlers) clientIP(r *http.Request) string {
	peer, ok := peerAddress(r)
	if !ok {
		return r.RemoteAddr
	}
	prefixes := h.trustedProxyPrefixes()
	if !addressTrusted(peer, prefixes) {
		return peer.String()
	}
	chain, ok := forwardedForChain(r)
	if !ok {
		return peer.String()
	}
	candidate := peer
	for index := len(chain) - 1; index >= 0; index-- {
		if !addressTrusted(candidate, prefixes) {
			return candidate.String()
		}
		candidate = chain[index]
	}
	return candidate.String()
}

func lastHeaderValue(value string) string {
	parts := strings.Split(value, ",")
	for index := len(parts) - 1; index >= 0; index-- {
		if value := strings.TrimSpace(parts[index]); value != "" {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func forwardedOrigin(r *http.Request) (string, bool) {
	proto := ""
	host := ""
	if element := lastHeaderValue(r.Header.Get("Forwarded")); element != "" {
		for _, parameter := range strings.Split(element, ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok {
				continue
			}
			switch strings.ToLower(key) {
			case "proto":
				proto = strings.Trim(value, `"`)
			case "host":
				host = strings.Trim(value, `"`)
			}
		}
	}
	if proto == "" {
		proto = lastHeaderValue(r.Header.Get("X-Forwarded-Proto"))
	}
	if host == "" {
		host = lastHeaderValue(r.Header.Get("X-Forwarded-Host"))
	}
	if proto != "http" && proto != "https" {
		return "", false
	}
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/\\@?#,\r\n\x00") {
		return "", false
	}
	parsed, err := url.Parse(proto + "://" + host)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return (&url.URL{Scheme: proto, Host: parsed.Host}).String(), true
}

func (h *handlers) externalOrigin(r *http.Request) (string, error) {
	if peer, ok := peerAddress(r); ok && addressTrusted(peer, h.trustedProxyPrefixes()) {
		if origin, ok := forwardedOrigin(r); ok {
			return origin, nil
		}
	}
	return externalOrigin(r)
}
