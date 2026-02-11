//go:build linux

package firewall

import (
	"testing"
	"time"
)

func TestParseNftTimeout(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"seconds", "30s", 30 * time.Second, false},
		{"minutes", "5m", 5 * time.Minute, false},
		{"hours", "2h", 2 * time.Hour, false},
		{"days", "1d", 24 * time.Hour, false},
		{"invalid unit", "10x", 0, true},
		{"empty", "", 0, true},
		{"no number", "s", 0, true},
		{"negative (invalid)", "-5m", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseNftTimeout(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error for input '%s', got nil", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for input '%s': %v", tt.input, err)
				}
				if result != tt.expected {
					t.Errorf("Expected %v, got %v", tt.expected, result)
				}
			}
		})
	}
}

func TestParseIP(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		byteLen  int // Expected byte length (4 for IPv4, 16 for IPv6)
	}{
		{"ipv4", "192.168.1.1", false, 4},
		{"ipv6", "2001:db8::1", false, 16},
		{"ipv4 loopback", "127.0.0.1", false, 4},
		{"ipv6 loopback", "::1", false, 16},
		{"invalid ip", "999.999.999.999", true, 0},
		{"invalid format", "not-an-ip", true, 0},
		{"cidr (should fail)", "10.0.0.0/8", true, 0},
		{"empty", "", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseIP(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error for input '%s', got nil", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for input '%s': %v", tt.input, err)
				}
				if len(result) != tt.byteLen {
					t.Errorf("Expected byte length %d, got %d for '%s'", tt.byteLen, len(result), tt.input)
				}
			}
		})
	}
}
