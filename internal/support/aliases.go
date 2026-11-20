package support

// Aliases: human handles for tester installs.
//
// The raw install_id (t-3f9c…) is fine for the hub, useless for a human and
// for an LLM agent reading logs. So the hub names every install that streams
// tester logs: tester-001, tester-002, … assigned at the first signal and never
// reused. An operator renames in the panel or through MCP; every surface —
// list, read, NDJSON export, MCP — shows the alias first and the install_id
// second.
//
// A reference is whatever someone typed to name one install: the alias
// «tester-003», its bare number «003», the install_id itself, or a unique
// prefix of either («t-3f9c», «tester-0»). Anything that names more than one
// install is refused instead of guessed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/m1d1x/hikkari-control/internal/db"
)

const (
	// aliasNumberWidth zero-pads the auto-assigned number: tester-001.
	aliasNumberWidth = 3
	// MaxAliasNote bounds the operator's note on an alias.
	MaxAliasNote = 500
	// assignAliasAttempts covers auto-assigned numbers an operator already took
	// by hand: each collision burns one number and tries the next.
	assignAliasAttempts = 16
)

var (
	ErrAliasNotFound    = errors.New("install not found")
	ErrAmbiguousInstall = errors.New("reference matches several installs")
	ErrAliasTaken       = errors.New("alias is already taken")
	ErrInvalidAlias     = errors.New("invalid alias")
)

var (
	// reAliasNumber matches the alias and its short forms: tester-003,
	// tester-3, 003.
	reAliasNumber = regexp.MustCompile(`^(?:tester-?)?(\d+)$`)
	// reAliasSlug bounds an operator-chosen alias.
	reAliasSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	reDigits    = regexp.MustCompile(`^\d+$`)
)

// formatAliasNumber builds the auto-assigned handle tester-NNN.
func formatAliasNumber(n int) string {
	return fmt.Sprintf("tester-%0*d", aliasNumberWidth, n)
}

// normalizeAliasRef folds the short forms of an alias — «tester-3», «003» —
// into the stored «tester-003». ok is false for anything else.
func normalizeAliasRef(ref string) (string, bool) {
	m := reAliasNumber.FindStringSubmatch(strings.ToLower(strings.TrimSpace(ref)))
	if m == nil {
		return "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return "", false
	}
	return formatAliasNumber(n), true
}

// ValidAlias reports whether an operator-chosen alias names one install
// without ambiguity: a lowercase slug, not all digits — «003» would read as
// the number form of the auto-assigned tester-003, not as a name of its own.
func ValidAlias(alias string) error {
	a := strings.ToLower(strings.TrimSpace(alias))
	switch {
	case a == "":
		return fmt.Errorf("%w: empty", ErrInvalidAlias)
	case reDigits.MatchString(a):
		return fmt.Errorf("%w: %q reads as the number form of tester-NNN", ErrInvalidAlias, a)
	case !reAliasSlug.MatchString(a):
		return fmt.Errorf("%w: %q (lowercase letters, digits, dashes; not all digits)", ErrInvalidAlias, a)
	}
	return nil
}

// aliasRow is one install with its handle, as the resolver sees it.
type aliasRow struct {
	installID string
	alias     string
}

// resolveAliasRef picks the install a reference names among rows: exact
// install_id first, then exact alias (its short forms included), then a unique
// prefix of either.
func resolveAliasRef(rows []aliasRow, ref string) (installID, alias string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", ErrAliasNotFound
	}
	for _, r := range rows {
		if r.installID == ref {
			return r.installID, r.alias, nil
		}
	}
	lower := strings.ToLower(ref)
	norm, isNumber := normalizeAliasRef(ref)
	for _, r := range rows {
		if r.alias == lower || (isNumber && r.alias == norm) {
			return r.installID, r.alias, nil
		}
	}
	var hit *aliasRow
	for i := range rows {
		r := rows[i]
		if !strings.HasPrefix(r.installID, ref) && !strings.HasPrefix(r.alias, lower) {
			continue
		}
		if hit != nil {
			return "", "", ErrAmbiguousInstall
		}
		hit = &rows[i]
	}
	if hit == nil {
		return "", "", ErrAliasNotFound
	}
	return hit.installID, hit.alias, nil
}

// AliasRequest renames one install (admin route, MCP rename tool).
type AliasRequest struct {
	Alias string `json:"alias"`
	Note  string `json:"note"`
}

// AliasRenameHandler renames the install the path names — by alias, by its
// number, by install_id or by a unique prefix of either.
func AliasRenameHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installID, _, err := store.ResolveInstall(r.Context(), r.PathValue("install"))
		if err != nil {
			writeResolveErr(w, err)
			return
		}
		var req AliasRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBody)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		alias, err := store.RenameAlias(r.Context(), installID, req.Alias, req.Note)
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidAlias):
				writeErr(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, ErrAliasTaken):
				writeErr(w, http.StatusConflict, err.Error())
			default:
				writeErr(w, http.StatusInternalServerError, "failed to rename install")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"install_id": installID,
			"alias":      alias,
			"note":       strings.TrimSpace(req.Note),
		})
	}
}

// TesterDeleteHandler takes the install the path names out of the tester list —
// by alias, by its number, by install_id or by a unique prefix of either. Its
// chunks and commands are dropped, and the product counters stop counting it:
// a dead test account is neither a tester nor a user.
func TesterDeleteHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installID, alias, err := store.ResolveInstall(r.Context(), r.PathValue("install"))
		if err != nil {
			writeResolveErr(w, err)
			return
		}
		if err := store.DeleteInstall(r.Context(), installID); err != nil {
			if errors.Is(err, ErrAliasNotFound) {
				writeErr(w, http.StatusNotFound, "install not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, "failed to delete install")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"install_id": installID,
			"alias":      alias,
		})
	}
}

// writeResolveErr maps the resolver's refusals to HTTP answers.
func writeResolveErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAliasNotFound):
		writeErr(w, http.StatusNotFound, "install not found")
	case errors.Is(err, ErrAmbiguousInstall):
		writeErr(w, http.StatusConflict, "reference matches several installs, be more specific")
	default:
		writeErr(w, http.StatusInternalServerError, "failed to resolve install")
	}
}

// ============================================================================
// In-Memory Store
// ============================================================================

// touchInstall names an install on first sight and records its last signal.
// A deleted install is not touched at all: hidden reports that an operator
// took it out of the list, so the caller keeps nothing for it either.
// The caller holds s.mu.
func (s *MemTesterStore) touchInstall(req TesterChunkRequest) (hidden bool) {
	in := s.installs[req.InstallID]
	if in == nil {
		var alias string
		for {
			s.aliasSeq++
			alias = formatAliasNumber(s.aliasSeq)
			if !s.aliasTaken(alias) {
				break
			}
		}
		in = &memInstall{alias: alias, firstSeen: s.now()}
		s.installs[req.InstallID] = in
	}
	if in.deleted {
		return true
	}
	in.platform, in.version, in.lastSeen = req.Platform, req.Version, s.now()
	return false
}

// aliasTaken reports whether any install holds alias. The caller holds s.mu.
func (s *MemTesterStore) aliasTaken(alias string) bool {
	for _, in := range s.installs {
		if in.alias == alias {
			return true
		}
	}
	return false
}

func (s *MemTesterStore) ResolveInstall(_ context.Context, ref string) (string, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]aliasRow, 0, len(s.installs))
	for id, in := range s.installs {
		if in.deleted {
			continue
		}
		rows = append(rows, aliasRow{installID: id, alias: in.alias})
	}
	return resolveAliasRef(rows, ref)
}

func (s *MemTesterStore) RenameAlias(_ context.Context, installID, alias, note string) (string, error) {
	if err := ValidAlias(alias); err != nil {
		return "", err
	}
	if len(note) > MaxAliasNote {
		return "", fmt.Errorf("%w: note longer than %d characters", ErrInvalidAlias, MaxAliasNote)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.installs[installID]
	if in == nil {
		return "", ErrAliasNotFound
	}
	a := strings.ToLower(strings.TrimSpace(alias))
	if a != in.alias && s.aliasTaken(a) {
		return "", ErrAliasTaken
	}
	in.alias, in.note = a, strings.TrimSpace(note)
	return a, nil
}

// DeleteInstall marks the install deleted and drops what it left behind: its
// chunks and queued commands. The row itself stays, so the number is never
// handed to another install.
func (s *MemTesterStore) DeleteInstall(_ context.Context, installID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.installs[installID]
	if in == nil || in.deleted {
		return ErrAliasNotFound
	}
	in.deleted = true
	kept := s.chunks[:0]
	for _, c := range s.chunks {
		if c.install != installID {
			kept = append(kept, c)
		}
	}
	s.chunks = kept
	s.commands = slices.DeleteFunc(s.commands, func(c *TesterCommandState) bool {
		return c.InstallID == installID
	})
	return nil
}

// ============================================================================
// PostgreSQL-backed Store
// ============================================================================

// touchInstallDB names an install on first sight and records its last signal.
// One statement per try: a chunk racing another first chunk of the same install
// falls through to the update, and an auto-assigned number an operator already
// took by hand burns that number and tries the next. An install an operator
// deleted is left alone — hidden says so, and the caller keeps nothing for it.
func touchInstallDB(ctx context.Context, e db.DBTX, req TesterChunkRequest) (hidden bool, err error) {
	for range assignAliasAttempts {
		var seen string
		err := e.QueryRow(ctx, `
			UPDATE support_install_aliases
			SET platform = $2, version = $3, last_seen_at = now()
			WHERE install_id = $1 AND deleted_at IS NULL
			RETURNING install_id
		`, req.InstallID, req.Platform, req.Version).Scan(&seen)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("touch install: %w", err)
		}
		var gone bool
		looked := e.QueryRow(ctx, `
			SELECT deleted_at IS NOT NULL FROM support_install_aliases WHERE install_id = $1
		`, req.InstallID).Scan(&gone)
		if looked == nil && gone {
			return true, nil
		}
		if looked != nil && !errors.Is(looked, pgx.ErrNoRows) {
			return false, fmt.Errorf("touch install: %w", looked)
		}
		if _, err := e.Exec(ctx, `
			INSERT INTO support_install_aliases (install_id, alias, platform, version)
			VALUES ($1, 'tester-' || lpad(nextval('support_install_alias_seq')::text, $4::int, '0'), $2, $3)
			ON CONFLICT DO NOTHING
		`, req.InstallID, req.Platform, req.Version, aliasNumberWidth); err != nil {
			return false, fmt.Errorf("assign alias: %w", err)
		}
	}
	return false, fmt.Errorf("assign alias: no free number after %d tries", assignAliasAttempts)
}

func (s *DBTesterStore) ResolveInstall(ctx context.Context, ref string) (string, string, error) {
	// Testers are counted in tens: reading them all keeps one resolver for both
	// stores instead of a second one in SQL.
	rows, err := s.pool.Query(ctx, `SELECT install_id, alias FROM support_install_aliases WHERE deleted_at IS NULL`)
	if err != nil {
		return "", "", fmt.Errorf("resolve install: %w", err)
	}
	defer rows.Close()
	var all []aliasRow
	for rows.Next() {
		var r aliasRow
		if err := rows.Scan(&r.installID, &r.alias); err != nil {
			return "", "", fmt.Errorf("scan alias row: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("iterate alias rows: %w", err)
	}
	return resolveAliasRef(all, ref)
}

func (s *DBTesterStore) RenameAlias(ctx context.Context, installID, alias, note string) (string, error) {
	if err := ValidAlias(alias); err != nil {
		return "", err
	}
	if len(note) > MaxAliasNote {
		return "", fmt.Errorf("%w: note longer than %d characters", ErrInvalidAlias, MaxAliasNote)
	}
	a := strings.ToLower(strings.TrimSpace(alias))
	tag, err := s.pool.Exec(ctx, `
		UPDATE support_install_aliases SET alias = $2, note = $3 WHERE install_id = $1
	`, installID, a, strings.TrimSpace(note))
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return "", ErrAliasTaken
		}
		return "", fmt.Errorf("rename alias: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrAliasNotFound
	}
	return a, nil
}

// DeleteInstall marks the install deleted and drops what it left behind: the
// log chunks Postgres keeps and its queued commands. The row stays, so the
// number is never handed to another install and the product counters keep
// knowing this install is not a person.
func (s *DBTesterStore) DeleteInstall(ctx context.Context, installID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE support_install_aliases SET deleted_at = now()
		WHERE install_id = $1 AND deleted_at IS NULL
	`, installID)
	if err != nil {
		return fmt.Errorf("delete install: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAliasNotFound
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM support_tester_log_chunks WHERE install_id = $1`, installID); err != nil {
		return fmt.Errorf("drop tester chunks: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM support_tester_commands WHERE install_id = $1`, installID); err != nil {
		return fmt.Errorf("drop tester commands: %w", err)
	}
	// The copy in the archive (internal/analytics) is left alone: the archive
	// is the owner's "замеры не удалять", and with the alias unresolvable those
	// lines are unreachable from every screen anyway.
	return nil
}

// scanCommand reads one command row; pgx.ErrNoRows means no such command.
func scanCommand(row pgx.Row) (TesterCommandState, error) {
	var c TesterCommandState
	err := row.Scan(&c.Nonce, &c.InstallID, &c.Kind, &c.CreatedAt, &c.DeliveredAt, &c.AckedAt)
	return c, err
}
