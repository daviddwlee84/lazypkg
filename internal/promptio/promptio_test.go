package promptio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportPreservesRenderedBytesAndRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.md")
	text := "# 診斷\n\nLiteral {{context_json}}\n"
	if err := Export(path, text); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != text {
		t.Fatal(string(b), err)
	}
	if err = Export(path, "replaced"); err == nil {
		t.Fatal("overwrote existing prompt")
	}
	b, _ = os.ReadFile(path)
	if string(b) != text {
		t.Fatal("existing prompt changed")
	}
}
