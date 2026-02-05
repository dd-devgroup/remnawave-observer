package geodata

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// caidaFixture — минимальный фрагмент файла CAIDA AS-Organizations.
const caidaFixture = `# Test CAIDA fixture
# AS lines: ASN|source|orgID|flags|description
# Org lines: O_<orgID>|source|orgName|country|parentOrgID
O_AS-CAIDA|CAIDA|CAIDA|US|
O_RU-SAMARA|RIPE|Samara Region Telecom|RU|
O_RU-BEE|RIPE|BEE Network LLC|RU|
O_KZ-TEL|RIPE|Kazakhstan Telecom|KZ|
1|ARIN|AS-CAIDA|Ua|CAIDA
34533|RIPE|RU-SAMARA|Ua|ESAMARA-AS
16345|RIPE|RU-BEE|Ua|BEE-AS Russia
12345|RIPE|O_MISSING|Ua|ORPHAN-AS
99001|RIPE|KZ-TEL|Ua|Kazakhtelecom AS
`

// writeGzFixture компрессирует текст и кладёт в dir/as-org2info.txt.gz.
func writeGzFixture(t *testing.T, dir, content string) {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	w.Close()
	if err := os.WriteFile(filepath.Join(dir, caidaLocalFilename), buf.Bytes(), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestAS2OrgLoader_ParseAndLookup(t *testing.T) {
	dir := t.TempDir()
	writeGzFixture(t, dir, caidaFixture)

	loader := NewAS2OrgLoader(dir, "", 7*24*time.Hour)
	if err := loader.loadFromFile(); err != nil {
		t.Fatalf("loadFromFile: %v", err)
	}

	if loader.Count() != 5 {
		t.Fatalf("expected 5 ASN entries, got %d", loader.Count())
	}

	// ASN 34533 → Org RU-SAMARA → "Samara Region Telecom", RU
	orgName, country, orgID, ok := loader.LookupOrgByASN(34533)
	if !ok {
		t.Fatal("ASN 34533 expected to be found")
	}
	if orgName != "Samara Region Telecom" {
		t.Errorf("orgName: got %q, want %q", orgName, "Samara Region Telecom")
	}
	if country != "RU" {
		t.Errorf("country: got %q, want %q", country, "RU")
	}
	if orgID != "RU-SAMARA" {
		t.Errorf("orgID: got %q, want %q", orgID, "RU-SAMARA")
	}

	// ASN 16345 → Org RU-BEE → "BEE Network LLC", RU
	orgName, country, _, ok = loader.LookupOrgByASN(16345)
	if !ok {
		t.Fatal("ASN 16345 expected to be found")
	}
	if orgName != "BEE Network LLC" {
		t.Errorf("orgName: got %q", orgName)
	}
	if country != "RU" {
		t.Errorf("country: got %q", country)
	}

	// ASN 99001 → Org KZ-TEL → "Kazakhstan Telecom", KZ
	orgName, country, _, ok = loader.LookupOrgByASN(99001)
	if !ok {
		t.Fatal("ASN 99001 expected to be found")
	}
	if orgName != "Kazakhstan Telecom" {
		t.Errorf("orgName: got %q", orgName)
	}
	if country != "KZ" {
		t.Errorf("country: got %q", country)
	}
}

func TestAS2OrgLoader_MissingASN(t *testing.T) {
	dir := t.TempDir()
	writeGzFixture(t, dir, caidaFixture)

	loader := NewAS2OrgLoader(dir, "", 7*24*time.Hour)
	if err := loader.loadFromFile(); err != nil {
		t.Fatalf("loadFromFile: %v", err)
	}

	_, _, _, ok := loader.LookupOrgByASN(77777)
	if ok {
		t.Error("ASN 77777 should not be found")
	}
}

func TestAS2OrgLoader_OrphanASN_FallbackToAutName(t *testing.T) {
	dir := t.TempDir()
	writeGzFixture(t, dir, caidaFixture)

	loader := NewAS2OrgLoader(dir, "", 7*24*time.Hour)
	if err := loader.loadFromFile(); err != nil {
		t.Fatalf("loadFromFile: %v", err)
	}

	// ASN 12345 maps to orgID "O_MISSING" — не существует в orgMap
	orgName, country, orgID, ok := loader.LookupOrgByASN(12345)
	if !ok {
		t.Fatal("ASN 12345 expected to be found")
	}
	if orgName != "ORPHAN-AS" {
		t.Errorf("orgName fallback: got %q, want AutName %q", orgName, "ORPHAN-AS")
	}
	if country != "" {
		t.Errorf("country for orphan: got %q, want empty", country)
	}
	if orgID != "O_MISSING" {
		t.Errorf("orgID: got %q", orgID)
	}
}

func TestAS2OrgLoader_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeGzFixture(t, dir, "# only comments\n# nothing else\n")

	loader := NewAS2OrgLoader(dir, "", 7*24*time.Hour)
	if err := loader.loadFromFile(); err != nil {
		t.Fatalf("loadFromFile: %v", err)
	}
	if loader.Count() != 0 {
		t.Errorf("expected 0 entries, got %d", loader.Count())
	}
}
