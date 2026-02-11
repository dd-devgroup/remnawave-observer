//go:build linux

package firewall

import (
	"net"
	"testing"
)

// TestCIDRCalculation проверяет вычисление первого и последнего IP в CIDR.
func TestCIDRCalculation(t *testing.T) {
	tests := []struct {
		name     string
		cidr     string
		wantFirst string
		wantLast  string
	}{
		{
			name:      "ipv4 /24",
			cidr:      "10.0.0.0/24",
			wantFirst: "10.0.0.0",
			wantLast:  "10.0.0.255",
		},
		{
			name:      "ipv4 /16",
			cidr:      "192.168.0.0/16",
			wantFirst: "192.168.0.0",
			wantLast:  "192.168.255.255",
		},
		{
			name:      "ipv4 /8",
			cidr:      "10.0.0.0/8",
			wantFirst: "10.0.0.0",
			wantLast:  "10.255.255.255",
		},
		{
			name:      "ipv4 /32 (single IP)",
			cidr:      "192.168.1.1/32",
			wantFirst: "192.168.1.1",
			wantLast:  "192.168.1.1",
		},
		{
			name:      "ipv6 /64",
			cidr:      "2001:db8::/64",
			wantFirst: "2001:db8::",
			wantLast:  "2001:db8::ffff:ffff:ffff:ffff",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ipNet, err := net.ParseCIDR(tt.cidr)
			if err != nil {
				t.Fatalf("ParseCIDR failed: %v", err)
			}

			// Вычисляем первый IP
			firstIP := ipNet.IP

			// Вычисляем последний IP
			lastIP := make(net.IP, len(firstIP))
			copy(lastIP, firstIP)
			for i := range lastIP {
				lastIP[i] = firstIP[i] | ^ipNet.Mask[i]
			}

			// Проверяем результаты
			gotFirst := firstIP.String()
			gotLast := lastIP.String()

			if gotFirst != tt.wantFirst {
				t.Errorf("First IP: got %s, want %s", gotFirst, tt.wantFirst)
			}
			if gotLast != tt.wantLast {
				t.Errorf("Last IP: got %s, want %s", gotLast, tt.wantLast)
			}
		})
	}
}

// TestInvalidCIDR проверяет обработку невалидных CIDR.
func TestInvalidCIDR(t *testing.T) {
	tests := []struct {
		name string
		cidr string
	}{
		{"invalid format", "not-a-cidr"},
		{"missing prefix", "10.0.0.0"},
		{"invalid prefix length", "10.0.0.0/33"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := net.ParseCIDR(tt.cidr)
			if err == nil {
				t.Errorf("Expected error for CIDR '%s', got nil", tt.cidr)
			}
		})
	}
}

// TestIncrementIP проверяет инкремент IP адреса для interval end marker.
func TestIncrementIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want string
	}{
		{"ipv4 simple", "10.0.0.1", "10.0.0.2"},
		{"ipv4 byte overflow", "10.0.0.255", "10.0.1.0"},
		{"ipv4 double overflow", "10.0.255.255", "10.1.0.0"},
		{"ipv4 max", "255.255.255.255", "0.0.0.0"}, // overflow wrap
		{"ipv6 simple", "2001:db8::1", "2001:db8::2"},
		{"ipv6 overflow", "2001:db8::ffff", "2001:db8::1:0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("ParseIP failed for %s", tt.ip)
			}

			result := incrementIP(ip)
			got := result.String()

			if got != tt.want {
				t.Errorf("incrementIP(%s) = %s, want %s", tt.ip, got, tt.want)
			}
		})
	}
}
