package iputil

import "net/netip"

// ParseSourceIP parses a source IP address.
func ParseSourceIP(value string) (netip.Addr, error) {
	return netip.ParseAddr(value)
}

// IsUnspecified reports whether the IP is a valid unspecified address
// like 0.0.0.0 or ::.
func IsUnspecified(value string) bool {
	addr, err := ParseSourceIP(value)
	if err != nil {
		return false
	}
	return addr.IsUnspecified()
}
