package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestMatrixPickNamesTheProfileAsTheHubStand(t *testing.T) {
	key := strings.Repeat("ab", 32)
	raw := []byte(`[
		{"label":"нидерланды-002","node":"unl1-2","capacityKey":"` + key + `","config":{"type":"hikkari"}},
		{"label":"рига-003","capacityKey":"` + key + `","config":{"type":"hysteria2"}},
		{"label":"amsterdam-1-002","config":{"type":"vless"}}
	]`)
	for label, want := range map[string]string{
		"нидерланды-002":  "unl1-2",
		"рига-003":        "profile-" + key[:12],
		"amsterdam-1-002": "amsterdam-1-002",
	} {
		ob, name, err := matrixPick(raw, label)
		if err != nil || ob == nil || name != want {
			t.Errorf("%s: name %q err %v, want %q", label, name, err, want)
		}
	}
	if _, _, err := matrixPick(raw, "нет-такого"); err == nil || !strings.Contains(err.Error(), "рига-003") {
		t.Errorf("missing profile: %v", err)
	}
}
