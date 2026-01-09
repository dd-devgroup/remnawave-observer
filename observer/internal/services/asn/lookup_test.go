package asn

import (
	"testing"
)

// Mock test - для реальных тестов нужна реальная MMDB база
// Эти тесты демонстрируют как использовать ASN lookup

func TestASNLookup_Structure(t *testing.T) {
	// Тест структуры - убедимся что все поля существуют
	lookup := &ASNLookup{
		dbPath: "/test/path.mmdb",
	}

	if lookup.dbPath != "/test/path.mmdb" {
		t.Errorf("Expected dbPath to be /test/path.mmdb, got %s", lookup.dbPath)
	}
}

func TestASNRecord_Structure(t *testing.T) {
	// Тест структуры записи ASN
	record := ASNRecord{
		AutonomousSystemNumber:       12389,
		AutonomousSystemOrganization: "Rostelecom",
	}

	if record.AutonomousSystemNumber != 12389 {
		t.Errorf("Expected ASN 12389, got %d", record.AutonomousSystemNumber)
	}

	if record.AutonomousSystemOrganization != "Rostelecom" {
		t.Errorf("Expected org 'Rostelecom', got %s", record.AutonomousSystemOrganization)
	}
}

// Примечание: Для полноценных интеграционных тестов нужна реальная база GeoLite2-ASN.mmdb
// Пример интеграционного теста (раскомментировать когда есть база):

/*
func TestASNLookup_RealDatabase(t *testing.T) {
	// Этот тест требует реальную базу GeoLite2-ASN.mmdb
	lookup, err := NewASNLookup("testdata/GeoLite2-ASN.mmdb")
	if err != nil {
		t.Skipf("Skipping test: database not found: %v", err)
		return
	}
	defer lookup.Close()

	tests := []struct {
		name        string
		ip          string
		expectASN   uint
		expectError bool
	}{
		{
			name:        "Google DNS",
			ip:          "8.8.8.8",
			expectASN:   15169, // Google
			expectError: false,
		},
		{
			name:        "Cloudflare DNS",
			ip:          "1.1.1.1",
			expectASN:   13335, // Cloudflare
			expectError: false,
		},
		{
			name:        "Private IP",
			ip:          "192.168.1.1",
			expectASN:   0,
			expectError: true, // Приватные IP не имеют ASN
		},
		{
			name:        "Invalid IP",
			ip:          "invalid",
			expectASN:   0,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asn, org, err := lookup.Lookup(tt.ip)
			
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error for %s, got none", tt.ip)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error for %s: %v", tt.ip, err)
				return
			}

			if asn != tt.expectASN {
				t.Errorf("Expected ASN %d for %s, got %d", tt.expectASN, tt.ip, asn)
			}

			if org == "" {
				t.Errorf("Expected non-empty organization for %s", tt.ip)
			}

			t.Logf("✅ %s: AS%d - %s", tt.ip, asn, org)
		})
	}
}

func TestASNLookup_LookupString(t *testing.T) {
	lookup, err := NewASNLookup("testdata/GeoLite2-ASN.mmdb")
	if err != nil {
		t.Skipf("Skipping test: database not found: %v", err)
		return
	}
	defer lookup.Close()

	asnStr, err := lookup.LookupString("8.8.8.8")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if asnStr != "AS15169" {
		t.Errorf("Expected AS15169, got %s", asnStr)
	}
}

func BenchmarkASNLookup(b *testing.B) {
	lookup, err := NewASNLookup("testdata/GeoLite2-ASN.mmdb")
	if err != nil {
		b.Skipf("Skipping benchmark: database not found: %v", err)
		return
	}
	defer lookup.Close()

	testIPs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"77.88.8.8",
		"208.67.222.222",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ip := testIPs[i%len(testIPs)]
		_, _, _ = lookup.Lookup(ip)
	}
}
*/

// Пример использования в документации
func ExampleASNLookup_Lookup() {
	// lookup, _ := NewASNLookup("/path/to/GeoLite2-ASN.mmdb")
	// defer lookup.Close()
	//
	// asn, org, err := lookup.Lookup("8.8.8.8")
	// if err != nil {
	//     log.Fatal(err)
	// }
	// 
	// fmt.Printf("ASN: %d, Organization: %s\n", asn, org)
	// Output: ASN: 15169, Organization: Google LLC
}
