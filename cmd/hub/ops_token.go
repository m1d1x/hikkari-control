package main

import (
	"fmt"
	"os"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

// runOpsToken issues or revokes a token of the ops plane (internal/auth/
// ops_token.go). Run on the box over SSH: cmd/ops does it by itself on its
// first call.
//
//	hub ops-token issue <label>    prints the token, once
//	hub ops-token revoke <label>   revokes every live token under the label
func runOpsToken(args []string) {
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "ops-token: %v\n", err)
		os.Exit(1)
	}
	if len(args) != 2 {
		fail(fmt.Errorf("usage: hub ops-token issue|revoke <label>"))
	}
	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()
	switch args[0] {
	case "issue":
		token, err := auth.IssueOpsToken(ctx, pool, args[1])
		if err != nil {
			fail(err)
		}
		fmt.Println(token)
	case "revoke":
		n, err := auth.RevokeOpsTokens(ctx, pool, args[1])
		if err != nil {
			fail(err)
		}
		fmt.Printf("revoked %d\n", n)
	default:
		fail(fmt.Errorf("usage: hub ops-token issue|revoke <label>"))
	}
	os.Exit(0)
}
