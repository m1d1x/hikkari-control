package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestGzipPartsJoinBack(t *testing.T) {
	f := filepath.Join(t.TempDir(), "core")
	raw := bytes.Repeat([]byte("sing-box "), 100000)
	if err := os.WriteFile(f, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	parts, err := gzipParts(f, 1000)
	if err != nil || len(parts) < 2 {
		t.Fatalf("parts %d err %v", len(parts), err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(bytes.Join(parts, nil)))
	if err != nil {
		t.Fatal(err)
	}
	back, err := io.ReadAll(gz)
	if err != nil || !bytes.Equal(back, raw) {
		t.Fatalf("joined parts differ: %v", err)
	}
}
