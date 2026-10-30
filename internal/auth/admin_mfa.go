package auth

// The admin plane's second step (roadmap task 77.2): what follows a right
// password, and what a signed-in operator does with their own factors.
//
// Login, from the panel's side:
//
//	POST /api/auth/admin/login            password → {step: "passkey"|"enroll", ceremonyId, options?}
//	POST /api/auth/admin/login/passkey    {ceremonyId, credential} → session
//	POST /api/auth/admin/login/recovery   {ceremonyId, code}       → session
//	POST /api/auth/admin/enroll/begin     {ceremonyId, code}       → registration options
//	POST /api/auth/admin/enroll/finish    {ceremonyId, credential, name} → session + recovery codes
//
// Signed in (behind admin.RequireAdmin):
//
//	GET    /api/v1/admin/security                      factors and step-up state
//	POST   /api/v1/admin/security/step-up/begin|finish one touch → 5 minutes of destructive rights
//	POST   /api/v1/admin/security/passkeys/begin|finish add a passkey (needs step-up)
//	DELETE /api/v1/admin/security/passkeys/{id}         remove one (needs step-up, never the last)
//	POST   /api/v1/admin/security/recovery-codes        ten new codes (needs step-up)
//	POST   /api/v1/admin/security/password              change own password (needs step-up)
//
// Every wrong second factor counts toward the same lockout as a wrong
// password: the counter is on the credential, and a guesser who already has
// the password is exactly who it exists to stop.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/httputil"
)

// beginAdminSecondFactor answers a right password with the step the operator
// owes. Called by AdminDirectLogin only.
func (h *Handlers) beginAdminSecondFactor(w http.ResponseWriter, r *http.Request, admin *Admin, binding DeviceBinding) {
	ctx := r.Context()
	if h.adminWA == nil {
		applog.Error(ctx, "auth", "admin login: second factor not configured")
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	device := adminCeremonyData{
		DevicePublicKey:   binding.PublicKey,
		DeviceFingerprint: binding.FingerprintHash,
		DevicePlatform:    binding.Platform,
	}

	user, err := loadAdminWAUser(ctx, h.pool, admin)
	if err != nil {
		applog.Error(ctx, "auth", "admin login: load passkeys", applog.F("err", err.Error()))
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}

	if len(user.creds) == 0 {
		pending, err := adminHasPendingEnrollment(ctx, h.pool, admin.ID)
		if err != nil {
			applog.Error(ctx, "auth", "admin login: enrol state", applog.F("err", err.Error()))
			writeError(w, http.StatusInternalServerError, "server error")
			return
		}
		if !pending {
			// No factor and no way to enrol one from here: only the CLI on
			// the box can issue a code. Said plainly, because the operator
			// who sees this needs to know where to go.
			applog.Warn(ctx, "auth", "admin login refused: no second factor",
				applog.F("admin_id", admin.ID))
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "second_factor_not_enrolled",
			})
			return
		}
		id, err := createAdminCeremony(ctx, h.pool, admin.ID, ceremonyEnroll, "", device)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "server error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"step": "enroll", "ceremonyId": id})
		return
	}

	assertion, session, err := h.adminWA.wa.BeginLogin(user)
	if err != nil {
		applog.Error(ctx, "auth", "admin login: begin passkey", applog.F("err", err.Error()))
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	device.Session = session
	id, err := createAdminCeremony(ctx, h.pool, admin.ID, ceremonyLogin, "", device)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"step":       "passkey",
		"ceremonyId": id,
		"options":    assertion.Response,
	})
}

type adminCeremonyRequest struct {
	CeremonyID string          `json:"ceremonyId"`
	Credential json.RawMessage `json:"credential"`
	Code       string          `json:"code"`
	Name       string          `json:"name"`
}

func decodeAdminCeremonyRequest(w http.ResponseWriter, r *http.Request) (adminCeremonyRequest, bool) {
	var req adminCeremonyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return req, false
	}
	return req, true
}

// AdminLoginPasskey handles POST /api/auth/admin/login/passkey.
func (h *Handlers) AdminLoginPasskey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, ok := decodeAdminCeremonyRequest(w, r)
	if !ok {
		return
	}
	adminID, data, err := takeAdminCeremony(ctx, h.pool, req.CeremonyID, ceremonyLogin, "")
	if err != nil {
		writeCeremonyError(w, err)
		return
	}
	admin, ok := h.liveAdminForSecondStep(w, r, adminID)
	if !ok {
		return
	}
	if data.Session == nil || h.adminWA == nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	cred, err := h.validateAdminAssertion(r, admin, *data.Session, req.Credential)
	if err != nil {
		applog.Warn(ctx, "auth", "admin login: passkey rejected",
			applog.F("admin_id", admin.ID), applog.F("err", err.Error()))
		h.failAdminSecondFactor(w, r, admin, http.StatusUnauthorized, "passkey_rejected")
		return
	}
	if err := recordAdminPasskeyUse(ctx, h.pool, admin.ID, cred); err != nil {
		applog.Warn(ctx, "auth", "admin login: record passkey use", applog.F("err", err.Error()))
	}
	h.mintAdminSession(w, r, admin, data, "passkey", nil)
}

// AdminLoginRecovery handles POST /api/auth/admin/login/recovery — the way in
// when no device holding a passkey is at hand.
func (h *Handlers) AdminLoginRecovery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, ok := decodeAdminCeremonyRequest(w, r)
	if !ok {
		return
	}
	adminID, err := peekAdminCeremony(ctx, h.pool, req.CeremonyID, ceremonyLogin)
	if err != nil {
		writeCeremonyError(w, err)
		return
	}
	admin, ok := h.liveAdminForSecondStep(w, r, adminID)
	if !ok {
		return
	}
	spent, err := consumeAdminRecoveryCode(ctx, h.pool, admin.ID, req.Code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	if !spent {
		// The ceremony stays: retype the code, not the password.
		h.failAdminSecondFactor(w, r, admin, http.StatusUnauthorized, "code_invalid")
		return
	}
	_, data, err := takeAdminCeremony(ctx, h.pool, req.CeremonyID, ceremonyLogin, "")
	if err != nil {
		writeCeremonyError(w, err)
		return
	}
	left, _ := CountAdminRecoveryCodesLeft(ctx, h.pool, admin.ID)
	audit.Log(ctx, h.pool, r, audit.Entry{
		AdminID: admin.ID, Action: "admin.login.recovery_code",
		TargetType: "admin", TargetID: admin.ID,
		Payload: map[string]any{"codesLeft": left},
	})
	applog.Warn(ctx, "auth", "admin login with a recovery code",
		applog.F("admin_id", admin.ID), applog.F("codes_left", left))
	h.mintAdminSession(w, r, admin, data, "recovery_code", map[string]any{"recoveryCodesLeft": left})
}

// AdminEnrollBegin handles POST /api/auth/admin/enroll/begin: the console code
// is checked, and registration options for the first passkey come back.
func (h *Handlers) AdminEnrollBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, ok := decodeAdminCeremonyRequest(w, r)
	if !ok {
		return
	}
	adminID, err := peekAdminCeremony(ctx, h.pool, req.CeremonyID, ceremonyEnroll)
	if err != nil {
		writeCeremonyError(w, err)
		return
	}
	admin, ok := h.liveAdminForSecondStep(w, r, adminID)
	if !ok {
		return
	}
	match, err := adminEnrollCodeMatches(ctx, h.pool, admin.ID, req.Code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	if !match {
		// The ceremony stays: retype the code, not the password.
		h.failAdminSecondFactor(w, r, admin, http.StatusUnauthorized, "code_invalid")
		return
	}
	_, data, err := takeAdminCeremony(ctx, h.pool, req.CeremonyID, ceremonyEnroll, "")
	if err != nil {
		writeCeremonyError(w, err)
		return
	}
	user, err := loadAdminWAUser(ctx, h.pool, admin)
	if err != nil || h.adminWA == nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	creation, session, err := h.adminWA.wa.BeginRegistration(user)
	if err != nil {
		applog.Error(ctx, "auth", "admin enrol: begin", applog.F("err", err.Error()))
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	data.Session = session
	id, err := createAdminCeremony(ctx, h.pool, admin.ID, ceremonyEnroll, "", data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremonyId": id, "options": creation.Response})
}

// AdminEnrollFinish handles POST /api/auth/admin/enroll/finish: the first
// passkey is stored, the console code is spent, ten recovery codes are issued
// and the operator is signed in.
func (h *Handlers) AdminEnrollFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, ok := decodeAdminCeremonyRequest(w, r)
	if !ok {
		return
	}
	adminID, data, err := takeAdminCeremony(ctx, h.pool, req.CeremonyID, ceremonyEnroll, "")
	if err != nil {
		writeCeremonyError(w, err)
		return
	}
	if data.Session == nil {
		// An enrol ceremony that never passed the code step.
		writeCeremonyError(w, ErrAdminCeremonyGone)
		return
	}
	admin, ok := h.liveAdminForSecondStep(w, r, adminID)
	if !ok {
		return
	}
	cred, err := h.createAdminCredential(r, admin, *data.Session, req.Credential)
	if err != nil {
		applog.Warn(ctx, "auth", "admin enrol: registration rejected",
			applog.F("admin_id", admin.ID), applog.F("err", err.Error()))
		writeError(w, http.StatusBadRequest, "passkey_rejected")
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	defer tx.Rollback(ctx)
	// The code is spent in the same transaction the passkey lands in: a code
	// that survives its own enrolment would enrol a second, unseen key.
	tag, err := tx.Exec(ctx, `
		UPDATE admins SET enroll_code_hash = NULL, enroll_code_expires_at = NULL, updated_at = now()
		WHERE id = $1 AND enroll_code_hash IS NOT NULL AND enroll_code_expires_at > now()`, admin.ID)
	if err != nil || tag.RowsAffected() != 1 {
		writeCeremonyError(w, ErrAdminCeremonyGone)
		return
	}
	if err := insertAdminPasskey(ctx, tx, admin.ID, passkeyName(req.Name), cred); err != nil {
		applog.Error(ctx, "auth", "admin enrol: store passkey", applog.F("err", err.Error()))
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	codes, err := issueAdminRecoveryCodes(ctx, tx, admin.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	audit.Log(ctx, h.pool, r, audit.Entry{
		AdminID: admin.ID, Action: "admin.passkey.enroll",
		TargetType: "admin", TargetID: admin.ID,
		Payload: map[string]any{"name": passkeyName(req.Name)},
	})
	h.mintAdminSession(w, r, admin, data, "enroll", map[string]any{"recoveryCodes": codes})
}

// ── signed-in security surface ───────────────────────────────────────────────

// AdminSecurity handles GET /api/v1/admin/security.
func (h *Handlers) AdminSecurity(w http.ResponseWriter, r *http.Request) {
	actor := GetAdmin(r)
	if actor == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	keys, err := ListAdminPasskeys(r.Context(), h.pool, actor.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	left, err := CountAdminRecoveryCodesLeft(r.Context(), h.pool, actor.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	resp := map[string]any{
		"login":             actor.Login,
		"role":              actor.Role,
		"passkeys":          keys,
		"recoveryCodesLeft": left,
	}
	if actor.SteppedUpUntil != nil && actor.SteppedUpUntil.After(time.Now()) {
		resp["steppedUpUntil"] = actor.SteppedUpUntil
	}
	writeJSON(w, http.StatusOK, resp)
}

// AdminStepUpBegin handles POST /api/v1/admin/security/step-up/begin.
func (h *Handlers) AdminStepUpBegin(w http.ResponseWriter, r *http.Request) {
	h.beginSessionAssertion(w, r, ceremonyStepUp)
}

// AdminStepUpFinish handles POST /api/v1/admin/security/step-up/finish.
func (h *Handlers) AdminStepUpFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := GetAdmin(r)
	if actor == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	req, ok := decodeAdminCeremonyRequest(w, r)
	if !ok {
		return
	}
	_, data, err := takeAdminCeremony(ctx, h.pool, req.CeremonyID, ceremonyStepUp, actor.SessionID)
	if err != nil {
		writeCeremonyError(w, err)
		return
	}
	admin, err := GetAdminByID(ctx, h.pool, actor.ID)
	if err != nil || data.Session == nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	cred, err := h.validateAdminAssertion(r, admin, *data.Session, req.Credential)
	if err != nil {
		applog.Warn(ctx, "auth", "admin step-up: passkey rejected",
			applog.F("admin_id", admin.ID), applog.F("err", err.Error()))
		h.failAdminSecondFactor(w, r, admin, http.StatusUnauthorized, "passkey_rejected")
		return
	}
	_ = recordAdminPasskeyUse(ctx, h.pool, admin.ID, cred)
	until, err := markAdminSteppedUp(ctx, h.pool, actor.SessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steppedUpUntil": until})
}

// AdminPasskeyAddBegin handles POST /api/v1/admin/security/passkeys/begin.
// Gated by step-up upstream: adding a key is adding a way in.
func (h *Handlers) AdminPasskeyAddBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := GetAdmin(r)
	if actor == nil || h.adminWA == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	admin, err := GetAdminByID(ctx, h.pool, actor.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	user, err := loadAdminWAUser(ctx, h.pool, admin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	// Exclude the keys already registered, so the browser refuses to enrol
	// the same authenticator twice instead of us storing a duplicate.
	exclude := make([]protocol.CredentialDescriptor, 0, len(user.creds))
	for _, c := range user.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, session, err := h.adminWA.wa.BeginRegistration(user, webauthn.WithExclusions(exclude))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	id, err := createAdminCeremony(ctx, h.pool, admin.ID, ceremonyAddKey, actor.SessionID,
		adminCeremonyData{Session: session})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremonyId": id, "options": creation.Response})
}

// AdminPasskeyAddFinish handles POST /api/v1/admin/security/passkeys/finish.
func (h *Handlers) AdminPasskeyAddFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := GetAdmin(r)
	if actor == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	req, ok := decodeAdminCeremonyRequest(w, r)
	if !ok {
		return
	}
	_, data, err := takeAdminCeremony(ctx, h.pool, req.CeremonyID, ceremonyAddKey, actor.SessionID)
	if err != nil {
		writeCeremonyError(w, err)
		return
	}
	admin, err := GetAdminByID(ctx, h.pool, actor.ID)
	if err != nil || data.Session == nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	cred, err := h.createAdminCredential(r, admin, *data.Session, req.Credential)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkey_rejected")
		return
	}
	if err := insertAdminPasskey(ctx, h.pool, admin.ID, passkeyName(req.Name), cred); err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	audit.Log(ctx, h.pool, r, audit.Entry{
		AdminID: admin.ID, Action: "admin.passkey.add",
		TargetType: "admin", TargetID: admin.ID,
		Payload: map[string]any{"name": passkeyName(req.Name)},
	})
	h.AdminSecurity(w, r)
}

// AdminPasskeyDelete handles DELETE /api/v1/admin/security/passkeys/{id}.
func (h *Handlers) AdminPasskeyDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := GetAdmin(r)
	if actor == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	switch err := DeleteAdminPasskey(ctx, h.pool, actor.ID, id); {
	case errors.Is(err, ErrAdminLastPasskey):
		writeError(w, http.StatusConflict, "last_passkey")
		return
	case errors.Is(err, ErrAdminCeremonyGone):
		writeError(w, http.StatusNotFound, "passkey not found")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	audit.Log(ctx, h.pool, r, audit.Entry{
		AdminID: actor.ID, Action: "admin.passkey.delete",
		TargetType: "admin", TargetID: actor.ID,
		Payload: map[string]any{"passkeyId": id},
	})
	h.AdminSecurity(w, r)
}

// AdminRecoveryCodesRegenerate handles POST /api/v1/admin/security/recovery-codes.
func (h *Handlers) AdminRecoveryCodesRegenerate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := GetAdmin(r)
	if actor == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	defer tx.Rollback(ctx)
	codes, err := issueAdminRecoveryCodes(ctx, tx, actor.ID)
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	audit.Log(ctx, h.pool, r, audit.Entry{
		AdminID: actor.ID, Action: "admin.recovery_codes.regenerate",
		TargetType: "admin", TargetID: actor.ID,
	})
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

type adminPasswordChangeRequest struct {
	NewPassword string `json:"newPassword"`
}

// AdminPasswordChange handles POST /api/v1/admin/security/password: the
// operator's own rotation, the everyday counterpart of the console's
// `admin-passwd` (which stays the way back for a forgotten password).
//
// Gated by step-up upstream and nothing more: a fresh Touch ID is the person
// at the machine, which is what a current-password field would only claim to
// prove. Asking for it anyway made the button useless in the one case anyone
// presses it — the password is forgotten (owner's call, 12.10).
//
// Refusals answer 422, never 401/403: the panel reads those as "session gone"
// and would throw the operator to the login screen over a typo.
func (h *Handlers) AdminPasswordChange(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := GetAdmin(r)
	if actor == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req adminPasswordChangeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.NewPassword) < AdminPasswordMinLen {
		writeError(w, http.StatusUnprocessableEntity, "password_too_short")
		return
	}
	admin, err := GetAdminByID(ctx, h.pool, actor.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	if CheckPassword(admin.PasswordHash, req.NewPassword) {
		writeError(w, http.StatusUnprocessableEntity, "password_unchanged")
		return
	}
	hash, err := HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	if err := SetAdminPassword(ctx, h.pool, admin.Login, hash); err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	revoked, err := RevokeOtherAdminSessions(ctx, h.pool, admin.ID, actor.SessionID, "password_changed")
	if err != nil {
		applog.Error(ctx, "auth", "admin password change: revoke other sessions",
			applog.F("admin_id", admin.ID), applog.F("err", err.Error()))
	}
	audit.Log(ctx, h.pool, r, audit.Entry{
		AdminID: admin.ID, Action: "admin.password.change",
		TargetType: "admin", TargetID: admin.ID,
		Payload: map[string]any{"sessionsRevoked": revoked},
	})
	writeJSON(w, http.StatusOK, map[string]any{"sessionsRevoked": revoked})
}

// ── helpers ──────────────────────────────────────────────────────────────────

// beginSessionAssertion starts a passkey assertion bound to the caller's
// session (step-up).
func (h *Handlers) beginSessionAssertion(w http.ResponseWriter, r *http.Request, purpose string) {
	ctx := r.Context()
	actor := GetAdmin(r)
	if actor == nil || h.adminWA == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	admin, err := GetAdminByID(ctx, h.pool, actor.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	user, err := loadAdminWAUser(ctx, h.pool, admin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	if len(user.creds) == 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "second_factor_not_enrolled"})
		return
	}
	assertion, session, err := h.adminWA.wa.BeginLogin(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	id, err := createAdminCeremony(ctx, h.pool, admin.ID, purpose, actor.SessionID,
		adminCeremonyData{Session: session})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremonyId": id, "options": assertion.Response})
}

func (h *Handlers) validateAdminAssertion(r *http.Request, admin *Admin, session webauthn.SessionData, raw json.RawMessage) (*webauthn.Credential, error) {
	if len(raw) == 0 {
		return nil, errors.New("credential missing")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse assertion: %w", err)
	}
	user, err := loadAdminWAUser(r.Context(), h.pool, admin)
	if err != nil {
		return nil, err
	}
	return h.adminWA.wa.ValidateLogin(user, session, parsed)
}

func (h *Handlers) createAdminCredential(r *http.Request, admin *Admin, session webauthn.SessionData, raw json.RawMessage) (*webauthn.Credential, error) {
	if len(raw) == 0 || h.adminWA == nil {
		return nil, errors.New("credential missing")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse registration: %w", err)
	}
	user, err := loadAdminWAUser(r.Context(), h.pool, admin)
	if err != nil {
		return nil, err
	}
	return h.adminWA.wa.CreateCredential(user, session, parsed)
}

// liveAdminForSecondStep re-reads the operator at the second step: a lockout,
// a disable or a password rotation between the two halves wins.
func (h *Handlers) liveAdminForSecondStep(w http.ResponseWriter, r *http.Request, adminID string) (*Admin, bool) {
	admin, err := GetAdminByID(r.Context(), h.pool, adminID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return nil, false
	}
	if admin.IsDisabled {
		writeError(w, http.StatusForbidden, "admin access required")
		return nil, false
	}
	if locked, until, err := IsAdminLocked(r.Context(), h.pool, admin.ID); err == nil && locked {
		writeLocked(w, until)
		return nil, false
	}
	return admin, true
}

// failAdminSecondFactor counts a wrong second factor toward the lockout.
func (h *Handlers) failAdminSecondFactor(w http.ResponseWriter, r *http.Request, admin *Admin, status int, code string) {
	locked, until, err := IncrementAdminFailedLogin(r.Context(), h.pool, admin.ID)
	if err != nil {
		applog.Error(r.Context(), "auth", "admin second factor: failure counter",
			applog.F("err", err.Error()))
	}
	if locked {
		writeLocked(w, until)
		return
	}
	writeJSON(w, status, map[string]string{"error": code})
}

func writeLocked(w http.ResponseWriter, until time.Time) {
	retryAfter := int(time.Until(until).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	writeError(w, http.StatusLocked, "account_temporarily_locked")
}

func writeCeremonyError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrAdminCeremonyGone) {
		// 410: the panel restarts from the password, which is the only
		// honest next step for a challenge that is no longer answerable.
		writeJSON(w, http.StatusGone, map[string]string{"error": "ceremony_expired"})
		return
	}
	writeError(w, http.StatusInternalServerError, "server error")
}

func passkeyName(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		return "passkey"
	}
	return s
}

// mintAdminSession is the end of every successful second step: the session,
// its cookies and the answer the panel signs in with. extra is merged into
// the answer (recovery codes at enrolment, codes left after a recovery login).
func (h *Handlers) mintAdminSession(w http.ResponseWriter, r *http.Request, admin *Admin, d adminCeremonyData, method string, extra map[string]any) {
	ctx := r.Context()
	clientIP := httputil.ClientIP(r)
	sessionID, err := CreateAdminSession(ctx, h.pool, CreateAdminSessionParams{
		AdminID:         admin.ID,
		PublicKey:       d.DevicePublicKey,
		FingerprintHash: d.DeviceFingerprint,
		Platform:        d.DevicePlatform,
		IP:              clientIP,
		UserAgent:       r.UserAgent(),
	})
	if err != nil {
		applog.Error(ctx, "auth", "admin login: create session failed",
			applog.F("err", err.Error()), applog.F("admin_id", admin.ID))
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	access, err := GenerateAdminAccessToken(admin.ID, admin.Login, sessionID, AdminAccessTTL, h.adminKeys.Private)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	SetAdminAccessCookie(w, r, access)
	SetAdminRefreshCookie(w, r, sessionID)
	if _, err := IssueAdminCSRFCookie(w, r); err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	if err := MarkAdminLoginSuccess(ctx, h.pool, admin.ID, clientIP); err != nil {
		applog.Warn(ctx, "auth", "admin login: mark success failed", applog.F("err", err.Error()))
	}
	applog.Info(ctx, "auth", "admin login success",
		applog.F("admin_id", admin.ID),
		applog.F("session_id", sessionID),
		applog.F("method", method),
		applog.F("ip", maskIP(clientIP)),
		applog.F("platform", d.DevicePlatform))

	resp := map[string]any{
		"sessionId": sessionID,
		"user": map[string]string{
			"id":       admin.ID,
			"username": admin.Login,
			"role":     string(admin.Role),
		},
	}
	for k, v := range extra {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}
