package validation

import (
	"fmt"
	"net/netip"
	"strings"
)

// ValidationResult содержит результат валидации.
type ValidationResult struct {
	Valid      bool
	ParsedAddr netip.Addr
	ParsedPfx  netip.Prefix
	IsCIDR     bool
	Error      string
}

// ValidateIPOrCIDR валидирует IP-адрес или CIDR подсеть через net/netip.
// Возвращает структуру ValidationResult с деталями валидации.
func ValidateIPOrCIDR(input string) ValidationResult {
	input = strings.TrimSpace(input)
	if input == "" {
		return ValidationResult{
			Valid: false,
			Error: "empty input",
		}
	}

	// Пытаемся распарсить как CIDR
	if strings.Contains(input, "/") {
		pfx, err := netip.ParsePrefix(input)
		if err != nil {
			return ValidationResult{
				Valid:  false,
				IsCIDR: true,
				Error:  fmt.Sprintf("invalid CIDR: %v", err),
			}
		}
		return ValidationResult{
			Valid:     true,
			ParsedPfx: pfx,
			IsCIDR:    true,
		}
	}

	// Пытаемся распарсить как IP-адрес
	addr, err := netip.ParseAddr(input)
	if err != nil {
		return ValidationResult{
			Valid: false,
			Error: fmt.Sprintf("invalid IP: %v", err),
		}
	}

	return ValidationResult{
		Valid:      true,
		ParsedAddr: addr,
		IsCIDR:     false,
	}
}

// IsValidIPOrCIDR — упрощённая функция для быстрой проверки валидности.
func IsValidIPOrCIDR(input string) bool {
	result := ValidateIPOrCIDR(input)
	return result.Valid
}
