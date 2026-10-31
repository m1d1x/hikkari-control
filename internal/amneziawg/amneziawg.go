package amneziawg

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// KeyPair represents a Curve25519 key pair in Base64 encoding.
type KeyPair struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

// ObfuscationParams holds AmneziaWG-specific obfuscation parameters.
type ObfuscationParams struct {
	Jc   uint16 `json:"jc"`
	Jmin uint16 `json:"jmin"`
	Jmax uint16 `json:"jmax"`
	S1   uint16 `json:"s1"`
	S2   uint16 `json:"s2"`
	H1   uint32 `json:"h1"`
	H2   uint32 `json:"h2"`
	H3   uint32 `json:"h3"`
	H4   uint32 `json:"h4"`
}

// ConfigOptions holds settings used to render an AmneziaWG configuration file.
type ConfigOptions struct {
	ClientIP         string            `json:"client_ip"`
	ClientPrivateKey string            `json:"client_private_key"`
	ServerPublicKey  string            `json:"server_public_key"`
	Endpoint         string            `json:"endpoint"`
	DNS              string            `json:"dns,omitempty"`
	Obfuscation      ObfuscationParams `json:"obfuscation"`
}

// GenerateKeyPair generates a standard WireGuard Curve25519 key pair.
// It generates 32 random bytes, applies clamping, computes the public key,
// and returns both keys encoded as standard base64 strings.
func GenerateKeyPair() (KeyPair, error) {
	var priv [32]byte
	if _, err := io.ReadFull(rand.Reader, priv[:]); err != nil {
		return KeyPair{}, fmt.Errorf("read random bytes: %w", err)
	}

	// Curve25519 clamping
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return KeyPair{}, fmt.Errorf("compute public key: %w", err)
	}

	return KeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(priv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// GenerateObfuscation generates randomized obfuscation parameters for AmneziaWG:
//   - Jc: 1..128
//   - Jmin: 40..100
//   - Jmax: 100..1280 (ensuring Jmax >= Jmin)
//   - S1: 15..150
//   - S2: 15..150
//   - H1..H4: random 32-bit uints, excluding 0 and standard WireGuard packet types (1..4)
func GenerateObfuscation() ObfuscationParams {
	jc := randUint16(1, 128)
	jmin := randUint16(40, 100)
	jmaxMin := uint16(100)
	if jmin > jmaxMin {
		jmaxMin = jmin
	}
	jmax := randUint16(jmaxMin, 1280)
	s1 := randUint16(15, 150)
	s2 := randUint16(15, 150)

	usedHeaders := make(map[uint32]bool, 4)
	h1 := randHeader(usedHeaders)
	h2 := randHeader(usedHeaders)
	h3 := randHeader(usedHeaders)
	h4 := randHeader(usedHeaders)

	return ObfuscationParams{
		Jc:   jc,
		Jmin: jmin,
		Jmax: jmax,
		S1:   s1,
		S2:   s2,
		H1:   h1,
		H2:   h2,
		H3:   h3,
		H4:   h4,
	}
}

// RenderConfig generates a valid INI configuration for an AmneziaWG client.
func RenderConfig(opts ConfigOptions) string {
	var b strings.Builder

	address := strings.TrimSpace(opts.ClientIP)
	if address != "" && !strings.Contains(address, "/") {
		address += "/32"
	}

	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", address)
	fmt.Fprintf(&b, "PrivateKey = %s\n", strings.TrimSpace(opts.ClientPrivateKey))
	if dns := strings.TrimSpace(opts.DNS); dns != "" {
		fmt.Fprintf(&b, "DNS = %s\n", dns)
	}
	fmt.Fprintf(&b, "Jc = %d\n", opts.Obfuscation.Jc)
	fmt.Fprintf(&b, "Jmin = %d\n", opts.Obfuscation.Jmin)
	fmt.Fprintf(&b, "Jmax = %d\n", opts.Obfuscation.Jmax)
	fmt.Fprintf(&b, "S1 = %d\n", opts.Obfuscation.S1)
	fmt.Fprintf(&b, "S2 = %d\n", opts.Obfuscation.S2)
	fmt.Fprintf(&b, "H1 = %d\n", opts.Obfuscation.H1)
	fmt.Fprintf(&b, "H2 = %d\n", opts.Obfuscation.H2)
	fmt.Fprintf(&b, "H3 = %d\n", opts.Obfuscation.H3)
	fmt.Fprintf(&b, "H4 = %d\n\n", opts.Obfuscation.H4)

	b.WriteString("[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", strings.TrimSpace(opts.ServerPublicKey))
	fmt.Fprintf(&b, "Endpoint = %s\n", strings.TrimSpace(opts.Endpoint))
	b.WriteString("AllowedIPs = 0.0.0.0/0, ::/0\n")
	b.WriteString("PersistentKeepalive = 25\n")

	return b.String()
}

func randUint16(minVal, maxVal uint16) uint16 {
	if minVal >= maxVal {
		return minVal
	}
	diff := big.NewInt(int64(maxVal - minVal + 1))
	n, err := rand.Int(rand.Reader, diff)
	if err != nil {
		panic(fmt.Sprintf("crypto/rand read failed: %v", err))
	}
	return minVal + uint16(n.Int64())
}

func randHeader(used map[uint32]bool) uint32 {
	var b [4]byte
	for {
		if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
			panic(fmt.Sprintf("crypto/rand read failed: %v", err))
		}
		val := binary.BigEndian.Uint32(b[:])
		if val == 0 || (val >= 1 && val <= 4) || used[val] {
			continue
		}
		used[val] = true
		return val
	}
}
