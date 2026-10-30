package cryptobox

import (
	"bytes"
	"strings"
	"testing"
)

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func mustConfigure(t *testing.T, env map[string]string) {
	t.Helper()
	resetForTests()
	if err := Configure(envFunc(env)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	mustConfigure(t, map[string]string{"NEKKO_CONTENT_KEY": key})

	cases := []string{
		"",
		"hello",
		strings.Repeat("a", 4096),
		"привет 🌸 какой-то текст",
		"\x00\x01\x02 binary",
	}
	for _, p := range cases {
		ct, err := EncryptString(p)
		if err != nil {
			t.Fatalf("encrypt %q: %v", p, err)
		}
		got, err := DecryptString(ct)
		if err != nil {
			t.Fatalf("decrypt %q: %v", p, err)
		}
		if got != p {
			t.Fatalf("round-trip mismatch: want %q got %q", p, got)
		}
	}
}

func TestNonceUnique(t *testing.T) {
	key, _ := GenerateKey()
	mustConfigure(t, map[string]string{"NEKKO_CONTENT_KEY": key})
	a, _ := EncryptString("same plaintext")
	b, _ := EncryptString("same plaintext")
	if bytes.Equal(a, b) {
		t.Fatal("encrypting the same plaintext twice produced identical envelopes — nonce reuse")
	}
}

func TestEmptyEnvelopeNoAlloc(t *testing.T) {
	resetForTests()
	if got, err := Decrypt(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty envelope must decrypt to empty: got %q err=%v", got, err)
	}
}

func TestRejectMissingKey(t *testing.T) {
	resetForTests()
	if _, err := EncryptString("x"); err != ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestKeyVersionRouting(t *testing.T) {
	v1, _ := GenerateKey()
	v2, _ := GenerateKey()
	// First, write under v1.
	mustConfigure(t, map[string]string{"NEKKO_CONTENT_KEY": v1})
	ct1, err := EncryptString("under v1")
	if err != nil {
		t.Fatal(err)
	}

	// Rotate: v2 active, v1 retained as legacy.
	mustConfigure(t, map[string]string{
		"NEKKO_CONTENT_KEY":         v2,
		"NEKKO_CONTENT_KEY_VERSION": "2",
		"NEKKO_CONTENT_KEY_V1":      v1,
	})
	if ActiveKeyVersion() != 2 {
		t.Fatalf("active version: want 2 got %d", ActiveKeyVersion())
	}
	got, err := DecryptString(ct1)
	if err != nil {
		t.Fatalf("decrypt v1 row after rotation: %v", err)
	}
	if got != "under v1" {
		t.Fatalf("want %q got %q", "under v1", got)
	}

	// Without the legacy key, v1 ciphertext must fail rather than
	// silently return junk.
	mustConfigure(t, map[string]string{
		"NEKKO_CONTENT_KEY":         v2,
		"NEKKO_CONTENT_KEY_VERSION": "2",
	})
	if _, err := DecryptString(ct1); err == nil {
		t.Fatal("expected ErrKeyVersionUnknown without v1 loaded")
	}
}

func TestRejectMalformed(t *testing.T) {
	key, _ := GenerateKey()
	mustConfigure(t, map[string]string{"NEKKO_CONTENT_KEY": key})

	if _, err := Decrypt([]byte{0x42}); err == nil {
		t.Fatal("expected error on truncated envelope")
	}
	// Wrong version byte.
	if _, err := Decrypt(bytes.Repeat([]byte{0xff}, MinEnvelopeSize)); err == nil {
		t.Fatal("expected error on unknown version")
	}
}

func TestKeyDecodeAcceptsBothBase64Forms(t *testing.T) {
	key, _ := GenerateKey() // std padded
	mustConfigure(t, map[string]string{"NEKKO_CONTENT_KEY": key})
	if !IsConfigured() {
		t.Fatal("not configured")
	}
	// Strip padding — RawStdEncoding form. Same key, should still load.
	resetForTests()
	if err := Configure(envFunc(map[string]string{"NEKKO_CONTENT_KEY": strings.TrimRight(key, "=")})); err != nil {
		t.Fatalf("raw-std form rejected: %v", err)
	}
}

func TestDeriveIsStablePerKeyAndLabel(t *testing.T) {
	resetForTests()
	if _, err := Derive("x"); err != ErrNotConfigured {
		t.Fatalf("derive without a key: got %v, want ErrNotConfigured", err)
	}

	key, _ := GenerateKey()
	mustConfigure(t, map[string]string{"NEKKO_CONTENT_KEY": key})
	a1, err := Derive("entry-batch")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := Derive("entry-batch")
	b, _ := Derive("other")
	if len(a1) != 32 || !bytes.Equal(a1, a2) {
		t.Fatal("same key and label must give the same 32 bytes")
	}
	if bytes.Equal(a1, b) {
		t.Fatal("different labels must give different keys")
	}

	// A restart with the same key derives the same secret — the property a
	// keyed choice persisted across restarts relies on.
	mustConfigure(t, map[string]string{"NEKKO_CONTENT_KEY": key})
	if again, _ := Derive("entry-batch"); !bytes.Equal(a1, again) {
		t.Fatal("derive changed across a reconfigure with the same key")
	}
	other, _ := GenerateKey()
	mustConfigure(t, map[string]string{"NEKKO_CONTENT_KEY": other})
	if rotated, _ := Derive("entry-batch"); bytes.Equal(a1, rotated) {
		t.Fatal("a different content key must derive a different secret")
	}
	if _, err := Derive(""); err == nil {
		t.Fatal("empty label accepted")
	}
}
