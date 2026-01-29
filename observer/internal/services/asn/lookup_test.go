package asn

import (
	"strings"
	"testing"
)

// Тесты для ASNLookup используют мок-данные через IPtoASNDatabase напрямую
// Интеграционные тесты с реальным скачиванием выполняются вручную

func TestASNLookup_WithMockData(t *testing.T) {
	// Создаем базу данных напрямую (без скачивания)
	db := NewIPtoASNDatabase()

	testData := `8.8.8.0	8.8.8.255	15169	US	GOOGLE
1.1.1.0	1.1.1.255	13335	AU	CLOUDFLARENET
77.88.8.0	77.88.8.255	13238	RU	YANDEX`

	reader := strings.NewReader(testData)
	if err := db.LoadFromReader(reader); err != nil {
		t.Fatalf("Failed to load test data: %v", err)
	}

	// Тестируем lookup напрямую через базу
	tests := []struct {
		name        string
		ip          string
		expectedASN uint32
		expectedOrg string
		wantErr     bool
	}{
		{
			name:        "Google DNS",
			ip:          "8.8.8.8",
			expectedASN: 15169,
			expectedOrg: "GOOGLE",
			wantErr:     false,
		},
		{
			name:        "Cloudflare DNS",
			ip:          "1.1.1.1",
			expectedASN: 13335,
			expectedOrg: "CLOUDFLARENET",
			wantErr:     false,
		},
		{
			name:        "Yandex DNS",
			ip:          "77.88.8.8",
			expectedASN: 13238,
			expectedOrg: "YANDEX",
			wantErr:     false,
		},
		{
			name:    "Private IP",
			ip:      "192.168.1.1",
			wantErr: true, // Приватные IP не имеют ASN
		},
		{
			name:    "Invalid IP",
			ip:      "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asn, org, err := db.Lookup(tt.ip)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error for %s, got none", tt.ip)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error for %s: %v", tt.ip, err)
				return
			}

			if asn != tt.expectedASN {
				t.Errorf("Expected ASN %d for %s, got %d", tt.expectedASN, tt.ip, asn)
			}

			if org != tt.expectedOrg {
				t.Errorf("Expected org '%s' for %s, got '%s'", tt.expectedOrg, tt.ip, org)
			}

			t.Logf("OK: %s -> AS%d (%s)", tt.ip, asn, org)
		})
	}
}

func TestASNLookup_LookupString_Mock(t *testing.T) {
	db := NewIPtoASNDatabase()

	testData := `8.8.8.0	8.8.8.255	15169	US	GOOGLE`
	reader := strings.NewReader(testData)
	if err := db.LoadFromReader(reader); err != nil {
		t.Fatalf("Failed to load test data: %v", err)
	}

	asnStr, err := db.LookupString("8.8.8.8")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if asnStr != "AS15169" {
		t.Errorf("Expected AS15169, got %s", asnStr)
	}
}

func TestASNLookup_LookupWithOrg_Mock(t *testing.T) {
	db := NewIPtoASNDatabase()

	testData := `8.8.8.0	8.8.8.255	15169	US	GOOGLE`
	reader := strings.NewReader(testData)
	if err := db.LoadFromReader(reader); err != nil {
		t.Fatalf("Failed to load test data: %v", err)
	}

	asnStr, org, err := db.LookupWithOrg("8.8.8.8")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if asnStr != "AS15169" {
		t.Errorf("Expected AS15169, got %s", asnStr)
	}

	if org != "GOOGLE" {
		t.Errorf("Expected GOOGLE, got %s", org)
	}
}

// Примечание: Для интеграционных тестов с реальным скачиванием
// раскомментируйте тест ниже и запустите вручную

/*
func TestASNLookup_Integration(t *testing.T) {
	// Этот тест скачивает реальную базу данных
	// Запускать только при необходимости проверки интеграции

	lookup, err := NewASNLookup("", 0) // Используем defaults
	if err != nil {
		t.Fatalf("Failed to create ASNLookup: %v", err)
	}
	defer lookup.Close()

	t.Logf("Database loaded with %d entries", lookup.Count())

	tests := []struct {
		ip          string
		expectedASN string
	}{
		{"8.8.8.8", "AS15169"},      // Google
		{"1.1.1.1", "AS13335"},      // Cloudflare
		{"77.88.8.8", "AS13238"},    // Yandex
	}

	for _, tt := range tests {
		asnStr, org, err := lookup.LookupWithOrg(tt.ip)
		if err != nil {
			t.Errorf("Error for %s: %v", tt.ip, err)
			continue
		}

		t.Logf("OK: %s -> %s (%s)", tt.ip, asnStr, org)

		if asnStr != tt.expectedASN {
			t.Errorf("Expected %s for %s, got %s", tt.expectedASN, tt.ip, asnStr)
		}
	}
}
*/

// Пример использования в документации
func ExampleASNLookup_LookupWithOrg() {
	// lookup, _ := NewASNLookup("", 0)
	// defer lookup.Close()
	//
	// asnStr, org, err := lookup.LookupWithOrg("8.8.8.8")
	// if err != nil {
	//     log.Fatal(err)
	// }
	//
	// fmt.Printf("ASN: %s, Organization: %s\n", asnStr, org)
	// Output: ASN: AS15169, Organization: GOOGLE
}
