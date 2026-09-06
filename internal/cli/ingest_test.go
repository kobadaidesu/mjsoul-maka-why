package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveEvidencePathNeverGuesses(t *testing.T) {
	dir := t.TempDir()
	glob := filepath.Join(dir, "*.meta.json")
	if p, err := resolveEvidencePath("explicit.json", glob, "--liqi-meta", "hint"); err != nil || p != "explicit.json" {
		t.Fatalf("explicit path not honored: %s %v", p, err)
	}
	if _, err := resolveEvidencePath("", glob, "--liqi-meta", "hint"); err == nil {
		t.Fatal("zero matches accepted")
	}
	one := filepath.Join(dir, "a.meta.json")
	if err := os.WriteFile(one, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if p, err := resolveEvidencePath("", glob, "--liqi-meta", "hint"); err != nil || p != one {
		t.Fatalf("unique match not used: %s %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.meta.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveEvidencePath("", glob, "--liqi-meta", "hint"); err == nil {
		t.Fatal("ambiguous matches accepted")
	}
}
