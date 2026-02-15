package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLuaScript_FromCustomPath(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "custom.lua")
	expected := "return 1"
	if err := os.WriteFile(scriptPath, []byte(expected), 0o600); err != nil {
		t.Fatalf("failed to create temp lua script: %v", err)
	}

	content, err := loadLuaScript("ignored.lua", scriptPath)
	if err != nil {
		t.Fatalf("loadLuaScript returned error: %v", err)
	}
	if string(content) != expected {
		t.Fatalf("unexpected script content: got %q, want %q", string(content), expected)
	}
}

func TestLoadLuaScript_DefaultLocations(t *testing.T) {
	t.Parallel()

	content, err := loadLuaScript("add_and_check_asn.lua")
	if err != nil {
		t.Fatalf("loadLuaScript returned error: %v", err)
	}
	if len(content) == 0 {
		t.Fatalf("script content must not be empty")
	}
}
