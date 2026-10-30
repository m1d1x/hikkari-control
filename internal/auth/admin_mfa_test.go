package auth

// The admin second factor end to end, against a real database and a software
// authenticator that speaks the same bytes a browser and Touch ID would
// (task 77.2). Skipped without TEST_DATABASE_URL.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
)

const (
	testAdminOrigin = "http://localhost:5174"
	testAdminRPID   = "localhost"
)

var b64 = base64.RawURLEncoding

// softKey is a passkey held in memory: one P-256 credential and a sign counter.
type softKey struct {
	priv    *ecdsa.PrivateKey
	credID  []byte
	counter uint32
	origin  string
	rpID    string
}

func newSoftKey(t *testing.T) *softKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{priv: priv, credID: id, origin: testAdminOrigin, rpID: testAdminRPID}
}

func (k *softKey) authData(attested bool) []byte {
	rpHash := sha256.Sum256([]byte(k.rpID))
	var buf bytes.Buffer
	buf.Write(rpHash[:])
	flags := byte(0x01 | 0x04) // user present, user verified
	if attested {
		flags |= 0x40
	}
	buf.WriteByte(flags)
	k.counter++
	_ = binary.Write(&buf, binary.BigEndian, k.counter)
	if attested {
		buf.Write(make([]byte, 16)) // AAGUID
		_ = binary.Write(&buf, binary.BigEndian, uint16(len(k.credID)))
		buf.Write(k.credID)
		x := k.priv.PublicKey.X.FillBytes(make([]byte, 32))
		y := k.priv.PublicKey.Y.FillBytes(make([]byte, 32))
		cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		buf.Write(cose)
	}
	return buf.Bytes()
}

func (k *softKey) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": typ, "challenge": challenge, "origin": k.origin, "crossOrigin": false,
	})
	return b
}

// create answers registration options the way navigator.credentials.create
// followed by toJSON() would.
func (k *softKey) create(t *testing.T, options map[string]any) json.RawMessage {
	t.Helper()
	cd := k.clientData("webauthn.create", options["challenge"].(string))
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": k.authData(true)})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.credID), "rawId": b64.EncodeToString(k.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	})
	return out
}

// get answers an assertion request the way navigator.credentials.get would.
func (k *softKey) get(t *testing.T, options map[string]any, userHandle []byte) json.RawMessage {
	t.Helper()
	cd := k.clientData("webauthn.get", options["challenge"].(string))
	ad := k.authData(false)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.credID), "rawId": b64.EncodeToString(k.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(userHandle),
		},
		"clientExtensionResults": map[string]any{},
	})
	return out
}

type mfaFixture struct {
	t       *testing.T
	h       *Handlers
	adminID string
	login   string
	pass    string
}

func newMFAFixture(t *testing.T) *mfaFixture {
	t.Helper()
	pool := sharedPool(t)
	adminKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(pool)
	h.SetAdminKeys(&KeySet{Private: adminKey, Public: &adminKey.PublicKey})
	wa, err := NewAdminWebAuthn(testAdminRPID, []string{testAdminOrigin})
	if err != nil {
		t.Fatal(err)
	}
	h.SetAdminWebAuthn(wa)

	login := "op-" + uuid.NewString()[:8]
	pass := "correct horse battery"
	hash, err := HashPassword(pass)
	if err != nil {
		t.Fatal(err)
	}
	id, err := CreateAdmin(context.Background(), pool, login, "", hash, AdminRoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	return &mfaFixture{t: t, h: h, adminID: id, login: login, pass: pass}
}

func (f *mfaFixture) post(handler http.HandlerFunc, body any, ctxAdmin *AdminInfo) (int, map[string]any, *http.Response) {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	if ctxAdmin != nil {
		req = req.WithContext(WithAdmin(req.Context(), ctxAdmin))
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Result()
}

func (f *mfaFixture) password(pass string) (int, map[string]any) {
	f.t.Helper()
	pub := make([]byte, DevicePublicKeySize)
	pub[0] = 0x04
	_, _ = rand.Read(pub[1:])
	fp := make([]byte, FingerprintHashSize)
	_, _ = rand.Read(fp)
	code, out, _ := f.post(f.h.AdminDirectLogin, map[string]any{
		"username": f.login, "password": pass,
		"device": map[string]any{
			"publicKey": b64.EncodeToString(pub), "fingerprintHash": b64.EncodeToString(fp), "platform": "web-test",
		},
	}, nil)
	return code, out
}

func hasAdminCookie(resp *http.Response) bool {
	for _, c := range resp.Cookies() {
		if c.Name == AdminAccessCookieName && c.Value != "" {
			return true
		}
	}
	return false
}

func optionsOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	o, ok := out["options"].(map[string]any)
	if !ok {
		t.Fatalf("no options in %v", out)
	}
	return o
}

// enroll walks the console-code path and returns the softkey and the recovery
// codes the panel would show.
func (f *mfaFixture) enroll(key *softKey) []string {
	t := f.t
	t.Helper()
	code, err := IssueAdminEnrollCode(context.Background(), f.h.pool, f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	st, out := f.password(f.pass)
	if st != http.StatusOK || out["step"] != "enroll" {
		t.Fatalf("password step = %d %v, want enroll", st, out)
	}
	cer := out["ceremonyId"].(string)

	// A typo costs an attempt, not the sign-in: the same ceremony still works.
	if st, out, _ := f.post(f.h.AdminEnrollBegin, map[string]any{"ceremonyId": cer, "code": "AAAA-AAAA-AAAA"}, nil); st != http.StatusUnauthorized || out["error"] != "code_invalid" {
		t.Fatalf("wrong enrol code = %d %v, want 401 code_invalid", st, out)
	}
	st, out, _ = f.post(f.h.AdminEnrollBegin, map[string]any{"ceremonyId": cer, "code": strings.ToLower(code)}, nil)
	if st != http.StatusOK {
		t.Fatalf("enrol begin = %d %v", st, out)
	}
	cred := key.create(t, optionsOf(t, out))
	st, out, resp := f.post(f.h.AdminEnrollFinish, map[string]any{"ceremonyId": out["ceremonyId"], "credential": cred, "name": "MacBook"}, nil)
	if st != http.StatusOK || !hasAdminCookie(resp) {
		t.Fatalf("enrol finish = %d %v (cookie %v)", st, out, hasAdminCookie(resp))
	}
	raw, _ := out["recoveryCodes"].([]any)
	if len(raw) != adminRecoveryCodeCount {
		t.Fatalf("recovery codes = %d, want %d", len(raw), adminRecoveryCodeCount)
	}
	codes := make([]string, len(raw))
	for i, c := range raw {
		codes[i] = c.(string)
	}
	return codes
}

func TestAdminPasswordAloneSignsNobodyIn(t *testing.T) {
	f := newMFAFixture(t)

	// No passkey and no console code: refused, and no cookie.
	st, out := f.password(f.pass)
	if st != http.StatusForbidden || out["error"] != "second_factor_not_enrolled" {
		t.Fatalf("password without any factor = %d %v, want 403 second_factor_not_enrolled", st, out)
	}

	key := newSoftKey(t)
	f.enroll(key)

	// Enrolled: the password step answers with a challenge, never a session.
	pub := make([]byte, DevicePublicKeySize)
	fp := make([]byte, FingerprintHashSize)
	_, out, resp := f.post(f.h.AdminDirectLogin, map[string]any{
		"username": f.login, "password": f.pass,
		"device": map[string]any{"publicKey": b64.EncodeToString(pub), "fingerprintHash": b64.EncodeToString(fp)},
	}, nil)
	if out["step"] != "passkey" || hasAdminCookie(resp) {
		t.Fatalf("password step after enrolment = %v (cookie %v), want passkey and no cookie", out, hasAdminCookie(resp))
	}
}

func TestAdminPasskeyLoginAndItsRefusals(t *testing.T) {
	f := newMFAFixture(t)
	key := newSoftKey(t)
	f.enroll(key)
	uid, _ := uuid.Parse(f.adminID)

	// The right key signs in.
	_, out := f.password(f.pass)
	cred := key.get(t, optionsOf(t, out), uid[:])
	st, body, resp := f.post(f.h.AdminLoginPasskey, map[string]any{"ceremonyId": out["ceremonyId"], "credential": cred}, nil)
	if st != http.StatusOK || !hasAdminCookie(resp) {
		t.Fatalf("passkey login = %d %v", st, body)
	}

	// The ceremony was spent: replaying the same answer is refused.
	if st, _, _ := f.post(f.h.AdminLoginPasskey, map[string]any{"ceremonyId": out["ceremonyId"], "credential": cred}, nil); st != http.StatusGone {
		t.Fatalf("replayed ceremony = %d, want 410", st)
	}

	// A key that was never registered is refused.
	stranger := newSoftKey(t)
	_, out = f.password(f.pass)
	st, _, resp = f.post(f.h.AdminLoginPasskey, map[string]any{"ceremonyId": out["ceremonyId"], "credential": stranger.get(t, optionsOf(t, out), uid[:])}, nil)
	if st == http.StatusOK || hasAdminCookie(resp) {
		t.Fatalf("unregistered key signed in (%d)", st)
	}

	// The right key on a look-alike origin is refused: the phishing case.
	phished := *key
	phished.origin = "http://evil.localhost:5174"
	_, out = f.password(f.pass)
	st, _, resp = f.post(f.h.AdminLoginPasskey, map[string]any{"ceremonyId": out["ceremonyId"], "credential": phished.get(t, optionsOf(t, out), uid[:])}, nil)
	if st == http.StatusOK || hasAdminCookie(resp) {
		t.Fatalf("assertion from a foreign origin signed in (%d)", st)
	}
}

func TestAdminRecoveryCodeIsOneUse(t *testing.T) {
	f := newMFAFixture(t)
	codes := f.enroll(newSoftKey(t))

	_, out := f.password(f.pass)
	st, body, resp := f.post(f.h.AdminLoginRecovery, map[string]any{"ceremonyId": out["ceremonyId"], "code": codes[3]}, nil)
	if st != http.StatusOK || !hasAdminCookie(resp) {
		t.Fatalf("recovery login = %d %v", st, body)
	}
	if left := body["recoveryCodesLeft"]; left != float64(adminRecoveryCodeCount-1) {
		t.Fatalf("codes left = %v, want %d", left, adminRecoveryCodeCount-1)
	}

	_, out = f.password(f.pass)
	if st, _, _ := f.post(f.h.AdminLoginRecovery, map[string]any{"ceremonyId": out["ceremonyId"], "code": codes[3]}, nil); st != http.StatusUnauthorized {
		t.Fatalf("spent code reused = %d, want 401", st)
	}
}

func TestAdminWrongSecondFactorsLockTheCredential(t *testing.T) {
	f := newMFAFixture(t)
	f.enroll(newSoftKey(t))

	_, out := f.password(f.pass)
	cer := out["ceremonyId"]
	var st int
	for i := 0; i < AdminLockoutThreshold; i++ {
		st, _, _ = f.post(f.h.AdminLoginRecovery, map[string]any{"ceremonyId": cer, "code": "0000-0000-0000"}, nil)
	}
	if st != http.StatusLocked {
		t.Fatalf("after %d wrong codes = %d, want 423", AdminLockoutThreshold, st)
	}
	if st, _ := f.password(f.pass); st != http.StatusLocked {
		t.Fatalf("password during lockout = %d, want 423", st)
	}
}

func TestAdminStepUpOpensAShortWindowOnThatSessionOnly(t *testing.T) {
	f := newMFAFixture(t)
	key := newSoftKey(t)
	f.enroll(key)
	uid, _ := uuid.Parse(f.adminID)

	_, out := f.password(f.pass)
	_, body, _ := f.post(f.h.AdminLoginPasskey, map[string]any{"ceremonyId": out["ceremonyId"], "credential": key.get(t, optionsOf(t, out), uid[:])}, nil)
	sessionID := body["sessionId"].(string)
	actor := &AdminInfo{ID: f.adminID, Login: f.login, SessionID: sessionID, Role: AdminRoleOwner}

	_, begin, _ := f.post(f.h.AdminStepUpBegin, nil, actor)
	// Another session cannot finish this session's step-up.
	other := *actor
	other.SessionID = uuid.NewString()
	if st, _, _ := f.post(f.h.AdminStepUpFinish, map[string]any{"ceremonyId": begin["ceremonyId"], "credential": key.get(t, optionsOf(t, begin), uid[:])}, &other); st != http.StatusGone {
		t.Fatalf("step-up finished by another session = %d, want 410", st)
	}

	_, begin, _ = f.post(f.h.AdminStepUpBegin, nil, actor)
	st, done, _ := f.post(f.h.AdminStepUpFinish, map[string]any{"ceremonyId": begin["ceremonyId"], "credential": key.get(t, optionsOf(t, begin), uid[:])}, actor)
	if st != http.StatusOK {
		t.Fatalf("step-up = %d %v", st, done)
	}
	gate, err := LoadAdminGateState(context.Background(), f.h.pool, sessionID, f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	info := &AdminInfo{SteppedUpUntil: gate.SteppedUpUntil}
	if !info.SteppedUp(time.Now()) {
		t.Fatal("session not stepped up after a fresh touch")
	}
	if info.SteppedUp(time.Now().Add(AdminStepUpWindow + time.Second)) {
		t.Fatal("step-up outlives its window")
	}
}

func TestAdminLastPasskeyCannotBeRemoved(t *testing.T) {
	f := newMFAFixture(t)
	f.enroll(newSoftKey(t))
	keys, err := ListAdminPasskeys(context.Background(), f.h.pool, f.adminID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("passkeys = %v %v", keys, err)
	}
	if err := DeleteAdminPasskey(context.Background(), f.h.pool, f.adminID, keys[0].ID); err != ErrAdminLastPasskey {
		t.Fatalf("removing the last passkey = %v, want ErrAdminLastPasskey", err)
	}
}

func TestAdminConsoleResetWipesFactorsAndSessions(t *testing.T) {
	f := newMFAFixture(t)
	key := newSoftKey(t)
	f.enroll(key)
	uid, _ := uuid.Parse(f.adminID)
	_, out := f.password(f.pass)
	_, body, _ := f.post(f.h.AdminLoginPasskey, map[string]any{"ceremonyId": out["ceremonyId"], "credential": key.get(t, optionsOf(t, out), uid[:])}, nil)
	sessionID := body["sessionId"].(string)

	if _, err := IssueAdminEnrollCode(context.Background(), f.h.pool, f.adminID); err != nil {
		t.Fatal(err)
	}
	if IsAdminSessionLive(context.Background(), f.h.pool, sessionID) {
		t.Fatal("session survived a second-factor reset")
	}
	if n, _ := CountAdminPasskeys(context.Background(), f.h.pool, f.adminID); n != 0 {
		t.Fatalf("passkeys after reset = %d", n)
	}
	if _, out := f.password(f.pass); out["step"] != "enroll" {
		t.Fatalf("password after reset = %v, want enroll", out)
	}
}

func TestAdminCodeNormalisation(t *testing.T) {
	for _, in := range []string{"7K3F-Q9WD-M2XA", "7k3fq9wdm2xa", " 7K3F Q9WD M2XA "} {
		if normalizeAdminCode(in) != "7K3FQ9WDM2XA" {
			t.Errorf("normalize(%q) = %q", in, normalizeAdminCode(in))
		}
	}
	if normalizeAdminCode("O0-Il") != "0011" {
		t.Errorf("look-alikes not folded: %q", normalizeAdminCode("O0-Il"))
	}
	c, err := newAdminCode()
	if err != nil || len(c) != 14 || strings.Count(c, "-") != 2 {
		t.Fatalf("newAdminCode = %q %v", c, err)
	}
}

func TestAdminPasswordChangeFromThePanel(t *testing.T) {
	f := newMFAFixture(t)
	key := newSoftKey(t)
	f.enroll(key)
	uid, _ := uuid.Parse(f.adminID)
	signIn := func() string {
		_, out := f.password(f.pass)
		_, body, _ := f.post(f.h.AdminLoginPasskey, map[string]any{"ceremonyId": out["ceremonyId"], "credential": key.get(t, optionsOf(t, out), uid[:])}, nil)
		return body["sessionId"].(string)
	}
	here, elsewhere := signIn(), signIn()
	actor := &AdminInfo{ID: f.adminID, Login: f.login, SessionID: here, Role: AdminRoleOwner}
	newPass := "a much longer passphrase"

	if st, out, _ := f.post(f.h.AdminPasswordChange, map[string]any{"newPassword": "short"}, actor); st != http.StatusUnprocessableEntity || out["error"] != "password_too_short" {
		t.Fatalf("short password = %d %v, want 422 password_too_short", st, out)
	}
	if st, out, _ := f.post(f.h.AdminPasswordChange, map[string]any{"newPassword": f.pass}, actor); st != http.StatusUnprocessableEntity || out["error"] != "password_unchanged" {
		t.Fatalf("same password = %d %v, want 422 password_unchanged", st, out)
	}
	// Three live sessions: the one enrolment opened, here and elsewhere.
	st, out, _ := f.post(f.h.AdminPasswordChange, map[string]any{"newPassword": newPass}, actor)
	if st != http.StatusOK || out["sessionsRevoked"] != float64(2) {
		t.Fatalf("password change = %d %v, want 200 with the two other sessions revoked", st, out)
	}
	if !IsAdminSessionLive(context.Background(), f.h.pool, here) {
		t.Fatal("the session that changed the password was signed out")
	}
	if IsAdminSessionLive(context.Background(), f.h.pool, elsewhere) {
		t.Fatal("another session survived the password change")
	}
	if st, _ := f.password(f.pass); st != http.StatusUnauthorized {
		t.Fatalf("old password at the door = %d, want 401", st)
	}
	if st, out := f.password(newPass); st != http.StatusOK || out["step"] != "passkey" {
		t.Fatalf("new password at the door = %d %v, want the passkey step", st, out)
	}
}
