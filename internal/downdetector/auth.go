package downdetector

import (
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"time"
)

// tokenKey is a token's hash. Tokens are kept and looked up only as hashes;
// the link between a token and a report is never stored or logged.
type tokenKey [32]byte

func hashToken(t string) tokenKey { return sha256.Sum256([]byte(t)) }

// Auth checks bearer tokens and limits requests per invite token.
type Auth struct {
	invite map[tokenKey]bool
	admin  map[tokenKey]bool

	rate  float64 // tokens per second
	burst float64

	mu      sync.Mutex
	buckets map[tokenKey]*tokenBucket
}

type tokenBucket struct {
	left float64
	at   time.Time
}

func NewAuth(s Secrets, perMin int) *Auth {
	a := &Auth{
		invite:  map[tokenKey]bool{},
		admin:   map[tokenKey]bool{},
		rate:    float64(perMin) / 60,
		burst:   float64(perMin),
		buckets: map[tokenKey]*tokenBucket{},
	}
	for _, t := range s.InviteTokens {
		a.invite[hashToken(t)] = true
	}
	for _, t := range s.AdminTokens {
		a.admin[hashToken(t)] = true
	}
	return a
}

func bearer(r *http.Request) (tokenKey, bool) {
	h := r.Header.Get("Authorization")
	t, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || t == "" {
		return tokenKey{}, false
	}
	return hashToken(t), true
}

// Invite returns the caller's invite token key, or false.
func (a *Auth) Invite(r *http.Request) (tokenKey, bool) {
	k, ok := bearer(r)
	return k, ok && a.invite[k]
}

func (a *Auth) Admin(r *http.Request) bool {
	k, ok := bearer(r)
	return ok && a.admin[k]
}

// Allow takes one request from the token's bucket.
func (a *Auth) Allow(k tokenKey, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.buckets[k]
	if b == nil {
		b = &tokenBucket{left: a.burst, at: now}
		a.buckets[k] = b
	}
	b.left = min(a.burst, b.left+now.Sub(b.at).Seconds()*a.rate)
	b.at = now
	if b.left < 1 {
		return false
	}
	b.left--
	return true
}
