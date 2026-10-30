package auth

// RSA keypair loader for RS256 JWT signing/verification.
//
// We moved from HS256 (single shared secret) to RS256 (asymmetric keypair)
// so that a downstream binary — cmd/sfu — can verify access tokens with only
// the public key. The private key stays scoped to the issuer, cmd/server.
//
// Env-var contract:
//   - JWT_PRIVATE_KEY       : PEM-encoded RSA private key (PKCS#1 or PKCS#8).
//   - JWT_PUBLIC_KEY        : PEM-encoded RSA public key (PKIX or PKCS#1).
//   - JWT_PRIVATE_KEY_FILE  : path to a file containing the PEM. Used when
//     the deployment delivers env via systemd EnvironmentFile or Docker
//     --env-file, neither of which supports multiline values. Wins over
//     the inline _KEY var when both are set.
//   - JWT_PUBLIC_KEY_FILE   : same idea for the public half.
//   - JWT_PUBLIC_ONLY=1     : skip the private-key load (verifier-only
//     nodes, e.g. a future cmd/sfu that doesn't mint).
//
// The admin plane reads the SAME contract under an ADMIN_JWT_ prefix
// (LoadAdminKeySet) — a different keypair entirely, so an admin token and an
// app token are not interchangeable no matter what any verifier forgets to
// check.
//
// File path is the recommended deployment shape — store PEM at
// /etc/nekko/jwt-priv.pem (mode 0600, owned by the service user) and
// reference it via JWT_PRIVATE_KEY_FILE.
//
// In dev, when neither env var is set, we generate a 2048-bit ephemeral
// pair at startup and log a warning. Sessions don't survive restart in
// that mode — same trade-off the old random-HMAC dev path had.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

// KeySet bundles the RSA keypair used for access JWT signing+verifying.
// Public is non-nil for any caller; Private is nil when JWT_PUBLIC_ONLY=1
// and the binary is a verifier-only role.
type KeySet struct {
	Private *rsa.PrivateKey
	Public  *rsa.PublicKey
}

// LoadKeySet reads JWT_PRIVATE_KEY / JWT_PUBLIC_KEY from the environment
// and returns the parsed pair. If both are unset, generates an ephemeral
// dev pair (2048-bit) and logs a loud warning. If PUBLIC_ONLY is set,
// only the public key is loaded.
func LoadKeySet() (*KeySet, error) {
	return loadKeySet("JWT")
}

// LoadAdminKeySet reads the SEPARATE keypair the admin plane signs with,
// from ADMIN_JWT_PRIVATE_KEY / ADMIN_JWT_PUBLIC_KEY (and their _FILE forms).
//
// Separate, and not a second audience on the same key, because an audience
// claim only holds while every verifier remembers to check it. A distinct
// keypair makes an admin token unverifiable by the app's verifier and an app
// token unverifiable by the admin gate as a property of the cryptography —
// the audience check on top of it (ParseAdminToken) is defence in depth, not
// the boundary itself.
//
// No public-only mode: cmd/server both mints and verifies these, and no other
// binary has any business doing either. A node that wanted to verify admin
// tokens would be a node that serves admin surface.
func LoadAdminKeySet() (*KeySet, error) {
	return loadKeySet("ADMIN_JWT")
}

func loadKeySet(prefix string) (*KeySet, error) {
	publicOnly := os.Getenv(prefix+"_PUBLIC_ONLY") == "1"

	privPEM, err := readKeyMaterial(prefix + "_PRIVATE_KEY")
	if err != nil {
		return nil, err
	}
	pubPEM, err := readKeyMaterial(prefix + "_PUBLIC_KEY")
	if err != nil {
		return nil, err
	}

	// Dev fallback: nothing set at all → generate ephemeral pair.
	if privPEM == "" && pubPEM == "" && !publicOnly {
		log.Printf("WARNING: %s_PRIVATE_KEY/%s_PUBLIC_KEY not set, generating ephemeral RSA-2048 keypair (sessions won't survive restart)", prefix, prefix)
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, fmt.Errorf("generate dev rsa key: %w", err)
		}
		return &KeySet{Private: priv, Public: &priv.PublicKey}, nil
	}

	ks := &KeySet{}

	// Public key: required in every mode. If only the private key is
	// supplied we derive the public from it; if only the public is
	// supplied we accept that (verifier-only).
	if pubPEM != "" {
		pub, err := parsePublicKey([]byte(pubPEM))
		if err != nil {
			return nil, fmt.Errorf("parse %s_PUBLIC_KEY: %w", prefix, err)
		}
		ks.Public = pub
	}

	if !publicOnly {
		if privPEM == "" {
			return nil, fmt.Errorf("%s_PRIVATE_KEY is required (set %s_PUBLIC_ONLY=1 for verifier-only nodes)", prefix, prefix)
		}
		priv, err := parsePrivateKey([]byte(privPEM))
		if err != nil {
			return nil, fmt.Errorf("parse %s_PRIVATE_KEY: %w", prefix, err)
		}
		ks.Private = priv
		if ks.Public == nil {
			ks.Public = &priv.PublicKey
		}
	}

	if ks.Public == nil {
		return nil, fmt.Errorf("%s_PUBLIC_KEY is required when %s_PUBLIC_ONLY=1", prefix, prefix)
	}
	return ks, nil
}

// parsePrivateKey accepts PKCS#1 ("RSA PRIVATE KEY") or PKCS#8
// ("PRIVATE KEY"). Most generators emit PKCS#8 by default; openssl
// genrsa emits PKCS#1.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is %T, want *rsa.PrivateKey", k)
		}
		return rk, nil
	default:
		return nil, fmt.Errorf("unsupported PEM type %q (want RSA PRIVATE KEY or PRIVATE KEY)", block.Type)
	}
}

// parsePublicKey accepts PKIX ("PUBLIC KEY") or PKCS#1 ("RSA PUBLIC KEY").
func parsePublicKey(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "PUBLIC KEY":
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rk, ok := k.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("PKIX key is %T, want *rsa.PublicKey", k)
		}
		return rk, nil
	case "RSA PUBLIC KEY":
		return x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM type %q (want PUBLIC KEY or RSA PUBLIC KEY)", block.Type)
	}
}

// readKeyMaterial returns the PEM bytes for a key, preferring the
// _FILE variant over the inline _KEY variant. The _FILE form exists
// because env-file loaders (systemd, docker --env-file) treat newlines
// as record separators, so a multiline PEM cannot be inlined safely.
func readKeyMaterial(envName string) (string, error) {
	if path := strings.TrimSpace(os.Getenv(envName + "_FILE")); path != "" {
		buf, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE (%s): %w", envName, path, err)
		}
		return string(buf), nil
	}
	return os.Getenv(envName), nil
}

// MarshalPublicKeyPEM exports a public key as PKIX PEM. Used by tooling
// that needs to hand the key to a verifier-only node (cmd/sfu future,
// or curl-based debugging).
func MarshalPublicKeyPEM(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}
