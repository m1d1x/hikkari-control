package main

// admin-create / admin-passwd / admin-disable: provisioning for the admin
// plane.
//
// There is no self-service signup for administrators and there is not meant to
// be one. An `admins` row is created out of band, by someone already on the
// machine with the database URL — which is the only bootstrap that does not
// require a credential to mint a credential.
//
// Usage:
//
//	cmd/server admin-create    [--role=owner|support] <login> [display name]
//	cmd/server admin-passwd    <login>
//	cmd/server admin-disable   <login>
//	cmd/server admin-2fa-reset <login>
//	cmd/server admin-role      <login> <owner|support>
//
// admin-create and admin-2fa-reset print a one-time ENROLMENT CODE. The panel
// asks for it after the password when the operator has no passkey yet; with it
// they register a passkey and receive ten recovery codes. This is the only way
// to enrol a first factor, so enrolling one requires being on the box — the
// SSH key is the root of trust of the whole admin plane (task 77.2).
//
// The password is read from stdin, never from a flag: an argument lands in the
// shell history and in the process table, where every other process on the box
// can read it.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
)

func runAdminCreate(args []string) {
	// A new operator is support unless told otherwise: the role that can do
	// less is the one you get by forgetting to choose.
	role := auth.AdminRoleSupport
	if len(args) > 0 && strings.HasPrefix(args[0], "--role=") {
		r, err := auth.ParseAdminRole(strings.TrimPrefix(args[0], "--role="))
		if err != nil {
			fmt.Fprintf(os.Stderr, "admin-create: %v\n", err)
			os.Exit(1)
		}
		role = r
		args = args[1:]
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: cmd/server admin-create [--role=owner|support] <login> [display name]")
		os.Exit(1)
	}
	login := strings.TrimSpace(args[0])
	displayName := strings.TrimSpace(strings.Join(args[1:], " "))
	if login == "" {
		fmt.Fprintln(os.Stderr, "admin-create: login must not be empty")
		os.Exit(1)
	}

	password := readAdminPassword("password for " + login)
	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-create: hash password: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()

	id, err := auth.CreateAdmin(ctx, pool, login, displayName, hash, role)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-create: %v\n", err)
		os.Exit(1)
	}
	code, err := auth.IssueAdminEnrollCode(ctx, pool, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-create: created %s, but issuing the enrolment code failed: %v\n", login, err)
		os.Exit(1)
	}
	fmt.Printf("admin created: %s (%s), role %s\n", login, id, role)
	printEnrollCode(code)
	os.Exit(0)
}

// runAdmin2FAReset wipes an operator's passkeys, recovery codes and sessions
// and prints a fresh enrolment code. The way back for an operator who lost
// every device AND their recovery codes — and the reason it is safe to offer:
// running it takes shell access to the box.
func runAdmin2FAReset(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: cmd/server admin-2fa-reset <login>")
		os.Exit(1)
	}
	login := strings.TrimSpace(args[0])

	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()

	admin, err := auth.GetAdminByLogin(ctx, pool, login)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-2fa-reset: %v\n", err)
		os.Exit(1)
	}
	code, err := auth.IssueAdminEnrollCode(ctx, pool, admin.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-2fa-reset: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("second factor reset for %s: passkeys, recovery codes and sessions removed\n", login)
	printEnrollCode(code)
	os.Exit(0)
}

// runAdminRole changes an operator's role and signs them out, so the new role
// applies from the next sign-in rather than to a session minted under the old
// one.
func runAdminRole(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cmd/server admin-role <login> <owner|support>")
		os.Exit(1)
	}
	login := strings.TrimSpace(args[0])
	role, err := auth.ParseAdminRole(strings.TrimSpace(args[1]))
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-role: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()

	admin, err := auth.GetAdminByLogin(ctx, pool, login)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-role: %v\n", err)
		os.Exit(1)
	}
	if _, err := pool.Exec(ctx, `UPDATE admins SET role = $2, updated_at = now() WHERE id = $1`, admin.ID, string(role)); err != nil {
		fmt.Fprintf(os.Stderr, "admin-role: %v\n", err)
		os.Exit(1)
	}
	n, err := auth.RevokeAllAdminSessions(ctx, pool, admin.ID, "role_changed")
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-role: role changed, session revoke failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s is now %s; %d session(s) revoked\n", login, role, n)
	os.Exit(0)
}

func printEnrollCode(code string) {
	fmt.Printf("\n  enrolment code: %s   (valid %.0f hours, one use)\n\n", code, auth.AdminEnrollCodeTTL.Hours())
	fmt.Println("  Open the panel (scripts/admin.sh), sign in with the password, enter this")
	fmt.Println("  code, touch Touch ID — then save the ten recovery codes it shows.")
}

func runAdminPasswd(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: cmd/server admin-passwd <login>")
		os.Exit(1)
	}
	login := strings.TrimSpace(args[0])

	password := readAdminPassword("new password for " + login)
	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-passwd: hash password: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()

	if err := auth.SetAdminPassword(ctx, pool, login, hash); err != nil {
		fmt.Fprintf(os.Stderr, "admin-passwd: %v\n", err)
		os.Exit(1)
	}

	// A rotated password almost always means "this credential may be
	// compromised", so end the live sessions too. Doing it here rather than
	// inside SetAdminPassword keeps the store honest about what it changed.
	admin, err := auth.GetAdminByLogin(ctx, pool, login)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-passwd: password changed, session revoke skipped: %v\n", err)
		os.Exit(0)
	}
	n, err := auth.RevokeAllAdminSessions(ctx, pool, admin.ID, "password_rotated")
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-passwd: password changed, session revoke failed: %v\n", err)
		os.Exit(0)
	}
	fmt.Printf("password updated for %s; %d session(s) revoked\n", login, n)
	os.Exit(0)
}

func runAdminDisable(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: cmd/server admin-disable <login>")
		os.Exit(1)
	}
	login := strings.TrimSpace(args[0])

	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()

	admin, err := auth.GetAdminByLogin(ctx, pool, login)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-disable: %v\n", err)
		os.Exit(1)
	}
	// Disable, never delete: admin_audit_log points at this row, and an audit
	// trail whose actors can vanish is not one.
	if _, err := pool.Exec(ctx, `UPDATE admins SET is_disabled = true, updated_at = now() WHERE id = $1`, admin.ID); err != nil {
		fmt.Fprintf(os.Stderr, "admin-disable: %v\n", err)
		os.Exit(1)
	}
	n, err := auth.RevokeAllAdminSessions(ctx, pool, admin.ID, "admin_disabled")
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin-disable: disabled, session revoke failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("admin disabled: %s; %d session(s) revoked\n", login, n)
	os.Exit(0)
}

// readAdminPassword reads one line from stdin and checks its length.
//
// Not a terminal-echo-suppressing prompt: these commands are meant to be run
// both by hand and piped (`printf %s "$PW" | cmd/server admin-create ops`),
// and a hard requirement for a TTY would break the piped form, which is the
// one a provisioning script uses.
func readAdminPassword(prompt string) string {
	fmt.Fprintf(os.Stderr, "%s: ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintf(os.Stderr, "\nread password: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr)
	password := strings.TrimRight(line, "\r\n")
	if len(password) < auth.AdminPasswordMinLen {
		fmt.Fprintf(os.Stderr, "password must be at least %d characters\n", auth.AdminPasswordMinLen)
		os.Exit(1)
	}
	return password
}

func adminCLIPool() (context.Context, context.CancelFunc, *pgxpool.Pool) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is empty")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	return ctx, cancel, pool
}
