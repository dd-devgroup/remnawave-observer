package geodata

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// caidaFixture — минимальный фрагмент файла CAIDA AS-Organizations (реальный формат).
const caidaFixture = `# Test CAIDA fixture
# format:org_id|changed|org_name|country|source
AS-CAIDA|20200101|CAIDA|US|ARIN
RU-SAMARA|20200101|Samara Region Telecom|RU|RIPE
RU-BEE|20200101|BEE Network LLC|RU|RIPE
KZ-TEL|20200101|Kazakhstan Telecom|KZ|RIPE
# format:aut|changed|aut_name|org_id|opaque_id|source
1|20200101|CAIDA|AS-CAIDA|AS1|ARIN
34533|20200101|ESAMARA-AS|RU-SAMARA|AS34533|RIPE
16345|20200101|BEE-AS Russia|RU-BEE|AS16345|RIPE
12345|20200101|ORPHAN-AS|O_MISSING|AS12345|RIPE
99001|20200101|Kazakhtelecom AS|KZ-TEL|AS99001|RIPE
`

// caidaRealFileFixture имитирует реальный файл: заголовочные комментарии и строки
// до первого «# format:» должны игнорироваться.
const caidaRealFileFixture = `# AS Org
# date: 202601
# program: build-as_org2info.pl
garbage_before_format|should|be|ignored|completely
# format:org_id|changed|org_name|country|source
RU-SAMARA|20200101|Samara Region Telecom|RU|RIPE
KZ-TEL|20200101|Kazakhstan Telecom|KZ|RIPE
# some mid-file comment
# format:aut|changed|aut_name|org_id|opaque_id|source
34533|20200101|ESAMARA-AS|RU-SAMARA|AS34533|RIPE
99001|20200101|Kazakhtelecom AS|KZ-TEL|AS99001|RIPE
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

// TestAS2OrgLoader_RealFormatParsing проверяет парсер на fixture, имитирующем реальный файл:
//   - заголовочные комментарии и строка до «# format:» игнорируются,
//   - оба раздела (org + as) парсятся корректно,
//   - LookupOrgByASN возвращает org_name/country из Org-раздела.
func TestAS2OrgLoader_RealFormatParsing(t *testing.T) {
	dir := t.TempDir()
	writeGzFixture(t, dir, caidaRealFileFixture)

	loader := NewAS2OrgLoader(dir, "", 7*24*time.Hour)
	if err := loader.loadFromFile(); err != nil {
		t.Fatalf("loadFromFile: %v", err)
	}

	// Только 2 AS строки в fixture
	if loader.Count() != 2 {
		t.Fatalf("expected 2 ASN entries, got %d", loader.Count())
	}

	// ASN 34533 → org_id RU-SAMARA → "Samara Region Telecom", RU
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

	// ASN 99001 → org_id KZ-TEL → "Kazakhstan Telecom", KZ
	orgName, country, orgID, ok = loader.LookupOrgByASN(99001)
	if !ok {
		t.Fatal("ASN 99001 expected to be found")
	}
	if orgName != "Kazakhstan Telecom" {
		t.Errorf("orgName: got %q, want %q", orgName, "Kazakhstan Telecom")
	}
	if country != "KZ" {
		t.Errorf("country: got %q, want %q", country, "KZ")
	}
	if orgID != "KZ-TEL" {
		t.Errorf("orgID: got %q, want %q", orgID, "KZ-TEL")
	}

	// garbage_before_format строка НЕ попала в orgMap (mode был modeNone)
	_, _, _, ok = loader.LookupOrgByASN(0)
	if ok {
		t.Error("ASN 0 must not exist — garbage lines before # format: must be ignored")
	}
}
