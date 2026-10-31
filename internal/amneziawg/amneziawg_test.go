package amneziawg

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestGenerateKeyPair(t *testing.T) {
	for i := 0; i < 50; i++ {
		kp, err := GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair() failed: %v", err)
		}

		// Check Base64 string lengths (32 bytes encoded in base64 is 44 chars)
		if len(kp.PrivateKey) != 44 {
			t.Errorf("expected PrivateKey length 44, got %d (%q)", len(kp.PrivateKey), kp.PrivateKey)
		}
		if len(kp.PublicKey) != 44 {
			t.Errorf("expected PublicKey length 44, got %d (%q)", len(kp.PublicKey), kp.PublicKey)
		}

		// Decode private key
		privBytes, err := base64.StdEncoding.DecodeString(kp.PrivateKey)
		if err != nil {
			t.Fatalf("failed to decode PrivateKey base64: %v", err)
		}
		if len(privBytes) != 32 {
			t.Fatalf("expected 32 bytes for PrivateKey, got %d", len(privBytes))
		}

		// Verify clamping: priv[0] &= 248, priv[31] &= 127, priv[31] |= 64
		if privBytes[0]&0b00000111 != 0 {
			t.Errorf("private key first byte clamping failed: lowest 3 bits must be 0, got %08b", privBytes[0])
		}
		if privBytes[31]&0b10000000 != 0 {
			t.Errorf("private key last byte clamping failed: bit 7 must be 0, got %08b", privBytes[31])
		}
		if privBytes[31]&0b01000000 == 0 {
			t.Errorf("private key last byte clamping failed: bit 6 must be 1, got %08b", privBytes[31])
		}

		// Decode public key
		pubBytes, err := base64.StdEncoding.DecodeString(kp.PublicKey)
		if err != nil {
			t.Fatalf("failed to decode PublicKey base64: %v", err)
		}
		if len(pubBytes) != 32 {
			t.Fatalf("expected 32 bytes for PublicKey, got %d", len(pubBytes))
		}

		// Verify public key matches curve25519 scalar multiplication of private key
		expectedPub, err := curve25519.X25519(privBytes, curve25519.Basepoint)
		if err != nil {
			t.Fatalf("curve25519 calculation failed: %v", err)
		}
		expectedPubBase64 := base64.StdEncoding.EncodeToString(expectedPub)
		if kp.PublicKey != expectedPubBase64 {
			t.Errorf("public key mismatch: got %q, expected %q", kp.PublicKey, expectedPubBase64)
		}
	}
}

func TestGenerateKeyPair_Uniqueness(t *testing.T) {
	seenPrivate := make(map[string]bool)
	seenPublic := make(map[string]bool)

	for i := 0; i < 100; i++ {
		kp, err := GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair() failed: %v", err)
		}
		if seenPrivate[kp.PrivateKey] {
			t.Fatalf("duplicate private key generated: %s", kp.PrivateKey)
		}
		if seenPublic[kp.PublicKey] {
			t.Fatalf("duplicate public key generated: %s", kp.PublicKey)
		}
		seenPrivate[kp.PrivateKey] = true
		seenPublic[kp.PublicKey] = true
	}
}

func TestGenerateObfuscation_RangesAndBoundaries(t *testing.T) {
	for i := 0; i < 1000; i++ {
		obf := GenerateObfuscation()

		// Jc: 1..128
		if obf.Jc < 1 || obf.Jc > 128 {
			t.Errorf("Jc out of range [1, 128]: got %d", obf.Jc)
		}

		// Jmin: 40..100
		if obf.Jmin < 40 || obf.Jmin > 100 {
			t.Errorf("Jmin out of range [40, 100]: got %d", obf.Jmin)
		}

		// Jmax: 100..1280
		if obf.Jmax < 100 || obf.Jmax > 1280 {
			t.Errorf("Jmax out of range [100, 1280]: got %d", obf.Jmax)
		}

		// Boundary condition: Jmin <= Jmax
		if obf.Jmin > obf.Jmax {
			t.Errorf("Jmin > Jmax: Jmin=%d, Jmax=%d", obf.Jmin, obf.Jmax)
		}

		// S1: 15..150
		if obf.S1 < 15 || obf.S1 > 150 {
			t.Errorf("S1 out of range [15, 150]: got %d", obf.S1)
		}

		// S2: 15..150
		if obf.S2 < 15 || obf.S2 > 150 {
			t.Errorf("S2 out of range [15, 150]: got %d", obf.S2)
		}

		// Headers H1..H4 != 0 and excluding WireGuard types 1..4
		headers := []uint32{obf.H1, obf.H2, obf.H3, obf.H4}
		seen := make(map[uint32]bool, 4)

		for idx, h := range headers {
			if h == 0 {
				t.Errorf("H%d is zero", idx+1)
			}
			if h >= 1 && h <= 4 {
				t.Errorf("H%d has standard WireGuard value %d", idx+1, h)
			}
			if seen[h] {
				t.Errorf("duplicate header value H%d = %d", idx+1, h)
			}
			seen[h] = true
		}
	}
}

func TestRenderConfig(t *testing.T) {
	obf := ObfuscationParams{
		Jc:   10,
		Jmin: 50,
		Jmax: 600,
		S1:   35,
		S2:   80,
		H1:   123456789,
		H2:   234567890,
		H3:   345678901,
		H4:   456789012,
	}

	opts := ConfigOptions{
		ClientIP:         "10.8.0.2",
		ClientPrivateKey: "cAAAAAAAAABBBBBBBBBCCCCCCCCCDDDDDDDDDEEEE=",
		ServerPublicKey:  "sFFFFFFFFFGGGGGGGGGHHHHHHHHHIIIIIIIIIJJJJ=",
		Endpoint:         "203.0.113.10:51820",
		DNS:              "1.1.1.1, 8.8.8.8",
		Obfuscation:      obf,
	}

	cfg := RenderConfig(opts)

	// Verify sections exist
	if !strings.Contains(cfg, "[Interface]") {
		t.Errorf("missing [Interface] section in:\n%s", cfg)
	}
	if !strings.Contains(cfg, "[Peer]") {
		t.Errorf("missing [Peer] section in:\n%s", cfg)
	}

	// Verify field substitution
	expectedLines := []string{
		"[Interface]",
		"Address = 10.8.0.2/32",
		"PrivateKey = cAAAAAAAAABBBBBBBBBCCCCCCCCCDDDDDDDDDEEEE=",
		"DNS = 1.1.1.1, 8.8.8.8",
		"Jc = 10",
		"Jmin = 50",
		"Jmax = 600",
		"S1 = 35",
		"S2 = 80",
		"H1 = 123456789",
		"H2 = 234567890",
		"H3 = 345678901",
		"H4 = 456789012",
		"[Peer]",
		"PublicKey = sFFFFFFFFFGGGGGGGGGHHHHHHHHHIIIIIIIIIJJJJ=",
		"Endpoint = 203.0.113.10:51820",
		"AllowedIPs = 0.0.0.0/0, ::/0",
		"PersistentKeepalive = 25",
	}

	for _, line := range expectedLines {
		if !strings.Contains(cfg, line) {
			t.Errorf("rendered config missing expected line: %q\nConfig:\n%s", line, cfg)
		}
	}

	// Verify exact structure match
	expectedConfig := fmt.Sprintf(`[Interface]
Address = 10.8.0.2/32
PrivateKey = %s
DNS = 1.1.1.1, 8.8.8.8
Jc = 10
Jmin = 50
Jmax = 600
S1 = 35
S2 = 80
H1 = 123456789
H2 = 234567890
H3 = 345678901
H4 = 456789012

[Peer]
PublicKey = %s
Endpoint = 203.0.113.10:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`, opts.ClientPrivateKey, opts.ServerPublicKey)

	if cfg != expectedConfig {
		t.Errorf("rendered config does not match expected output.\nGot:\n%s\nExpected:\n%s", cfg, expectedConfig)
	}
}

func TestRenderConfig_WithoutDNS(t *testing.T) {
	opts := ConfigOptions{
		ClientIP:         "10.8.0.5/32", // already has /32
		ClientPrivateKey: "cAAAAAAAAABBBBBBBBBCCCCCCCCCDDDDDDDDDEEEE=",
		ServerPublicKey:  "sFFFFFFFFFGGGGGGGGGHHHHHHHHHIIIIIIIIIJJJJ=",
		Endpoint:         "203.0.113.10:51820",
		DNS:              "", // empty DNS
		Obfuscation: ObfuscationParams{
			Jc:   5,
			Jmin: 45,
			Jmax: 200,
			S1:   20,
			S2:   25,
			H1:   100,
			H2:   200,
			H3:   300,
			H4:   400,
		},
	}

	cfg := RenderConfig(opts)

	if strings.Contains(cfg, "DNS =") {
		t.Errorf("config should not contain 'DNS =' when DNS is empty, got:\n%s", cfg)
	}

	// Address must remain 10.8.0.5/32 and not 10.8.0.5/32/32
	if !strings.Contains(cfg, "Address = 10.8.0.5/32") {
		t.Errorf("expected 'Address = 10.8.0.5/32', got:\n%s", cfg)
	}
	if strings.Contains(cfg, "10.8.0.5/32/32") {
		t.Errorf("unexpected duplicated subnet mask '/32/32'")
	}
}
