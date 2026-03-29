package storage

import "testing"

func TestParseCheckResultStatus0WithAllItems(t *testing.T) {
	raw := []interface{}{
		int64(0),
		int64(2),
		int64(1),
		[]interface{}{"AS123", "AS456"},
	}

	res, err := parseCheckResult(raw, "u1")
	if err != nil {
		t.Fatalf("parseCheckResult returned error: %v", err)
	}

	if res.StatusCode != 0 {
		t.Fatalf("expected status 0, got %d", res.StatusCode)
	}
	if !res.IsNew {
		t.Fatalf("expected IsNew=true")
	}
	if res.CurrentCount != 2 {
		t.Fatalf("expected CurrentCount=2, got %d", res.CurrentCount)
	}
	if len(res.AllUserItems) != 2 {
		t.Fatalf("expected 2 AllUserItems, got %d", len(res.AllUserItems))
	}
}

func TestParseCheckResultStatus0LegacyFormat(t *testing.T) {
	raw := []interface{}{
		int64(0),
		int64(1),
		int64(1),
	}

	res, err := parseCheckResult(raw, "u2")
	if err != nil {
		t.Fatalf("parseCheckResult returned error: %v", err)
	}

	if res.StatusCode != 0 {
		t.Fatalf("expected status 0, got %d", res.StatusCode)
	}
	if !res.IsNew {
		t.Fatalf("expected IsNew=true")
	}
	if res.CurrentCount != 1 {
		t.Fatalf("expected CurrentCount=1, got %d", res.CurrentCount)
	}
	if len(res.AllUserItems) != 0 {
		t.Fatalf("expected empty AllUserItems for legacy format, got %d", len(res.AllUserItems))
	}
}

func TestParseCheckResultUnexpectedStatus(t *testing.T) {
	raw := []interface{}{
		int64(1),
		[]interface{}{"AS10", "AS20", "AS30"},
	}

	res, err := parseCheckResult(raw, "u3")
	if err == nil {
		t.Fatal("expected error for unexpected legacy status, got nil")
	}
	if res != nil {
		t.Fatalf("expected nil result on error, got %#v", res)
	}
}
