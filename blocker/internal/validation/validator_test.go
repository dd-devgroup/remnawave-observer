package validation

import (
	"testing"
)

func TestValidateIPOrCIDR_ValidIPv4(t *testing.T) {
	tests := []string{
		"192.168.1.1",
		"10.0.0.1",
		"8.8.8.8",
		"255.255.255.255",
		"0.0.0.0",
	}

	for _, ip := range tests {
		result := ValidateIPOrCIDR(ip)
		if !result.Valid {
			t.Errorf("Expected %s to be valid IPv4, but got invalid: %s", ip, result.Error)
		}
		if result.IsCIDR {
			t.Errorf("Expected %s to be IP (not CIDR), but got CIDR", ip)
		}
	}
}

func TestValidateIPOrCIDR_ValidIPv6(t *testing.T) {
	tests := []string{
		"2001:db8::1",
		"::1",
		"fe80::1",
		"2001:0db8:85a3:0000:0000:8a2e:0370:7334",
	}

	for _, ip := range tests {
		result := ValidateIPOrCIDR(ip)
		if !result.Valid {
			t.Errorf("Expected %s to be valid IPv6, but got invalid: %s", ip, result.Error)
		}
		if result.IsCIDR {
			t.Errorf("Expected %s to be IP (not CIDR), but got CIDR", ip)
		}
	}
}

func TestValidateIPOrCIDR_ValidCIDR(t *testing.T) {
	tests := []string{
		"192.168.1.0/24",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"2001:db8::/32",
		"fe80::/10",
	}

	for _, cidr := range tests {
		result := ValidateIPOrCIDR(cidr)
		if !result.Valid {
			t.Errorf("Expected %s to be valid CIDR, but got invalid: %s", cidr, result.Error)
		}
		if !result.IsCIDR {
			t.Errorf("Expected %s to be CIDR, but got IP", cidr)
		}
	}
}

func TestValidateIPOrCIDR_Invalid(t *testing.T) {
	tests := []string{
		"999.999.999.999",
		"192.168.1",
		"192.168.1.1.1",
		"invalid",
		"192.168.1.1/",
		"192.168.1.0/33",
		"not-an-ip",
		"",
		"   ",
		"1.2.3.4; rm -rf /", // Injection attempt
		"192.168.1.1 && cat /etc/passwd",
	}

	for _, input := range tests {
		result := ValidateIPOrCIDR(input)
		if result.Valid {
			t.Errorf("Expected %s to be invalid, but got valid", input)
		}
		if result.Error == "" {
			t.Errorf("Expected error message for invalid input %s", input)
		}
	}
}

func TestIsValidIPOrCIDR(t *testing.T) {
	validTests := []string{
		"192.168.1.1",
		"10.0.0.0/8",
		"2001:db8::1",
	}

	for _, input := range validTests {
		if !IsValidIPOrCIDR(input) {
			t.Errorf("Expected %s to be valid", input)
		}
	}

	invalidTests := []string{
		"invalid",
		"",
		"999.999.999.999",
	}

	for _, input := range invalidTests {
		if IsValidIPOrCIDR(input) {
			t.Errorf("Expected %s to be invalid", input)
		}
	}
}
