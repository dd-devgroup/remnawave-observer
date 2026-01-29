package asn

import (
	"strings"
	"testing"
)

// Тестовые данные в формате iptoasn.com TSV
const testTSVData = `1.0.0.0	1.0.0.255	13335	AU	CLOUDFLARENET
1.0.1.0	1.0.3.255	0	None	Not routed
1.0.4.0	1.0.7.255	38803	AU	WPL-AS-AP
8.8.8.0	8.8.8.255	15169	US	GOOGLE
1.1.1.0	1.1.1.255	13335	AU	CLOUDFLARENET
77.88.8.0	77.88.8.255	13238	RU	YANDEX
`

func TestIPtoUint32(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		expected uint32
		wantErr  bool
	}{
		{
			name:     "Valid IP 0.0.0.0",
			ip:       "0.0.0.0",
			expected: 0,
			wantErr:  false,
		},
		{
			name:     "Valid IP 255.255.255.255",
			ip:       "255.255.255.255",
			expected: 4294967295,
			wantErr:  false,
		},
		{
			name:     "Valid IP 8.8.8.8",
			ip:       "8.8.8.8",
			expected: 134744072, // 8*16777216 + 8*65536 + 8*256 + 8
			wantErr:  false,
		},
		{
			name:     "Valid IP 1.0.0.1",
			ip:       "1.0.0.1",
			expected: 16777217, // 1*16777216 + 1
			wantErr:  false,
		},
		{
			name:    "Invalid IP",
			ip:      "invalid",
			wantErr: true,
		},
		{
			name:    "IPv6 address",
			ip:      "::1",
			wantErr: true,
		},
		{
			name:    "Empty string",
			ip:      "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ipToUint32(tt.ip)

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

			if result != tt.expected {
				t.Errorf("Expected %d for %s, got %d", tt.expected, tt.ip, result)
			}
		})
	}
}

func TestIPtoASNDatabase_LoadFromReader(t *testing.T) {
	db := NewIPtoASNDatabase()

	reader := strings.NewReader(testTSVData)
	err := db.LoadFromReader(reader)

	if err != nil {
		t.Fatalf("Failed to load database: %v", err)
	}

	count := db.Count()
	if count != 6 {
		t.Errorf("Expected 6 entries, got %d", count)
	}

	if !db.IsLoaded() {
		t.Error("Database should be loaded")
	}
}

func TestIPtoASNDatabase_LoadFromReader_Empty(t *testing.T) {
	db := NewIPtoASNDatabase()

	reader := strings.NewReader("")
	err := db.LoadFromReader(reader)

	if err == nil {
		t.Error("Expected error for empty database")
	}
}

func TestIPtoASNDatabase_Lookup(t *testing.T) {
	db := NewIPtoASNDatabase()
	reader := strings.NewReader(testTSVData)
	if err := db.LoadFromReader(reader); err != nil {
		t.Fatalf("Failed to load database: %v", err)
	}

	tests := []struct {
		name        string
		ip          string
		expectedASN uint32
		expectedOrg string
		wantErr     bool
	}{
		{
			name:        "Google DNS 8.8.8.8",
			ip:          "8.8.8.8",
			expectedASN: 15169,
			expectedOrg: "GOOGLE",
			wantErr:     false,
		},
		{
			name:        "Cloudflare 1.1.1.1",
			ip:          "1.1.1.1",
			expectedASN: 13335,
			expectedOrg: "CLOUDFLARENET",
			wantErr:     false,
		},
		{
			name:        "Cloudflare range start 1.0.0.0",
			ip:          "1.0.0.0",
			expectedASN: 13335,
			expectedOrg: "CLOUDFLARENET",
			wantErr:     false,
		},
		{
			name:        "Cloudflare range end 1.0.0.255",
			ip:          "1.0.0.255",
			expectedASN: 13335,
			expectedOrg: "CLOUDFLARENET",
			wantErr:     false,
		},
		{
			name:        "Yandex DNS 77.88.8.8",
			ip:          "77.88.8.8",
			expectedASN: 13238,
			expectedOrg: "YANDEX",
			wantErr:     false,
		},
		{
			name:    "Not routed IP 1.0.1.1",
			ip:      "1.0.1.1",
			wantErr: true, // ASN = 0 означает "Not routed"
		},
		{
			name:    "IP not in database",
			ip:      "192.168.1.1",
			wantErr: true,
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
					t.Errorf("Expected error for %s, got ASN=%d, Org=%s", tt.ip, asn, org)
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
				t.Errorf("Expected org %s for %s, got %s", tt.expectedOrg, tt.ip, org)
			}
		})
	}
}

func TestIPtoASNDatabase_LookupString(t *testing.T) {
	db := NewIPtoASNDatabase()
	reader := strings.NewReader(testTSVData)
	if err := db.LoadFromReader(reader); err != nil {
		t.Fatalf("Failed to load database: %v", err)
	}

	asnStr, err := db.LookupString("8.8.8.8")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if asnStr != "AS15169" {
		t.Errorf("Expected AS15169, got %s", asnStr)
	}
}

func TestIPtoASNDatabase_LookupWithOrg(t *testing.T) {
	db := NewIPtoASNDatabase()
	reader := strings.NewReader(testTSVData)
	if err := db.LoadFromReader(reader); err != nil {
		t.Fatalf("Failed to load database: %v", err)
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

func TestIPtoASNDatabase_EmptyDatabase(t *testing.T) {
	db := NewIPtoASNDatabase()

	_, _, err := db.Lookup("8.8.8.8")
	if err == nil {
		t.Error("Expected error for empty database")
	}
}

func TestIPtoASNDatabase_BinarySearchEdgeCases(t *testing.T) {
	// Тест на граничные случаи binary search
	db := NewIPtoASNDatabase()
	reader := strings.NewReader(testTSVData)
	if err := db.LoadFromReader(reader); err != nil {
		t.Fatalf("Failed to load database: %v", err)
	}

	// IP в середине диапазона
	asn, _, err := db.Lookup("8.8.8.128")
	if err != nil {
		t.Errorf("Unexpected error for 8.8.8.128: %v", err)
	}
	if asn != 15169 {
		t.Errorf("Expected ASN 15169 for 8.8.8.128, got %d", asn)
	}

	// IP на границе диапазона (начало)
	asn, _, err = db.Lookup("8.8.8.0")
	if err != nil {
		t.Errorf("Unexpected error for 8.8.8.0: %v", err)
	}
	if asn != 15169 {
		t.Errorf("Expected ASN 15169 for 8.8.8.0, got %d", asn)
	}

	// IP на границе диапазона (конец)
	asn, _, err = db.Lookup("8.8.8.255")
	if err != nil {
		t.Errorf("Unexpected error for 8.8.8.255: %v", err)
	}
	if asn != 15169 {
		t.Errorf("Expected ASN 15169 for 8.8.8.255, got %d", asn)
	}
}

func BenchmarkIPtoASNDatabase_Lookup(b *testing.B) {
	db := NewIPtoASNDatabase()
	reader := strings.NewReader(testTSVData)
	if err := db.LoadFromReader(reader); err != nil {
		b.Fatalf("Failed to load database: %v", err)
	}

	testIPs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"77.88.8.8",
		"1.0.0.100",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ip := testIPs[i%len(testIPs)]
		_, _, _ = db.Lookup(ip)
	}
}

func BenchmarkIPToUint32(b *testing.B) {
	testIPs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"192.168.1.1",
		"255.255.255.255",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ip := testIPs[i%len(testIPs)]
		_, _ = ipToUint32(ip)
	}
}
