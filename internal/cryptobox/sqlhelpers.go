package cryptobox

// SQL-glue helpers. Kept in their own file so call sites import a single
// well-known surface and don't have to know the envelope layout.

// DecryptOrPlain returns the decrypted string from `ct` if it is a
// non-empty envelope; otherwise falls back to `plain` (the legacy
// plaintext column). Used during the rollout window where some rows
// have been backfilled and others still hold plaintext.
//
// Pass `nil` for `plain` when the row simply has no fallback (e.g. the
// legacy column was already dropped). An empty `ct` plus nil/empty
// `plain` returns "" with nil error — that's the "no content" case.
func DecryptOrPlain(ct []byte, plain *string) (string, error) {
	if len(ct) > 0 {
		return DecryptString(ct)
	}
	if plain != nil {
		return *plain, nil
	}
	return "", nil
}

// EncryptForWrite is the dual: encrypts `s` with the active key and
// returns the envelope plus the active key version, ready to bind into
// an INSERT/UPDATE alongside a NULL for the legacy plaintext column.
//
// Returns an empty slice (and version 0) for empty input so the caller
// can pass a SQL NULL with `nil` instead of an empty BYTEA blob.
func EncryptForWrite(s string) ([]byte, byte, error) {
	if s == "" {
		return nil, 0, nil
	}
	ct, err := EncryptString(s)
	if err != nil {
		return nil, 0, err
	}
	return ct, ActiveKeyVersion(), nil
}
