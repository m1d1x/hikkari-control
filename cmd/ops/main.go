// Command ops is the agent's and the operator's way to the tester stream on the
// hub: who is on the line, their logs, a diagnostic on request, errors, tickets,
// CI and release state. The same commands run from a shell (scripts/ops) and as
// an MCP server over stdio (`ops mcp`, server name hikkari-ops).
//
// It reaches the hub the way the panel does: an SSH tunnel to 127.0.0.1:9443 on
// the box, carried by the operator's key, opened on demand. The token of the
// ops plane comes from HIKKARI_OPS_TOKEN or ~/.config/hikkari/ops-token; with
// neither, it is issued on the box over the same key and saved there.
//
// Environment: HIKKARI_OPS_HOST (ssh destination, default hikkari-main),
// HIKKARI_OPS_URL (a hub to call directly, no tunnel: local stacks and tests).
package main

import (
	"fmt"
	"os"
)

const usage = `usage: ops <command>
  testers                           who streams logs: alias, version, OS, last signal
  logs <who> [--last 1h] [--from T] [--to T] [--src PREFIX] [--tail N]
  diag <who> [--kind run_doctor|snapshot]
                                    ask the client for a diagnostic, wait, print it
  probe <who> --targets label=ip:port,label=ip:port
                                    TCP flows from the tester's card, past the tunnel
  load <who>                        a [diag:load] round over the tester's profiles now
  matrix --sub URL --profile LABEL --knob fp=chrome,firefox [--vantage spb,lv] [--rounds 3]
                                    one knob of a profile, measured from Russia and outside
  census add <url> --name X         save a paid subscription, list its entries
  census probe [--who 002,005]      TCP count from St Petersburg and testers
  census add-addr --name X --addrs ip:port,ip:port
                                    a service with no import: network and TCP only
  census exit-link --name X         a link to open through the VPN: its exit lands in report
  census proto --who 002,005        try the profiles through a core on the tester
  census watch --who 002,005 [--every 1h]
                                    probe and proto on a schedule, until stopped
  census report                     their entries and our relays, network by network
  rename <who> <alias> [--note TEXT]
  errors [--window 24h|7d|30d]      client error groups
  tickets [--status open]           support reports
  ticket <id>                       one report with its logs
  ci [--repo client|control] [--ref main] [--limit 10]
  release                           versions the update channels serve
  mcp                               MCP server over stdio
<who>: tester-003, 003, an install id, or a unique prefix of either`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, rest := os.Args[1], os.Args[2:]
	// «census add» in the shell is the tool census-add.
	if cmd == "census" && len(rest) > 0 {
		cmd, rest = "census-"+rest[0], rest[1:]
	}
	ops := newClient()
	defer ops.close()
	if cmd == "census-watch" {
		if err := ops.censusWatch(rest); err != nil {
			fmt.Fprintln(os.Stderr, err)
			ops.close()
			os.Exit(1)
		}
		return
	}
	if cmd == "mcp" {
		if err := serveMCP(ops, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			ops.close()
			os.Exit(1)
		}
		return
	}
	args, err := parseCLI(cmd, rest)
	if err == nil {
		var out string
		out, err = ops.call(cmd, args)
		// A command can fail part-way with what it measured so far (matrix at
		// one vantage): that is printed before the error, never dropped.
		if out != "" {
			fmt.Println(out)
		}
		if err == nil {
			return
		}
	}
	fmt.Fprintln(os.Stderr, err)
	ops.close()
	os.Exit(1)
}

// parseCLI turns command-line words into the same arguments the MCP tools take:
// positional words by the command's names, --name value for the rest.
func parseCLI(cmd string, words []string) (map[string]string, error) {
	tool, ok := toolsByCommand()[cmd]
	if !ok {
		return nil, fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
	args := map[string]string{}
	next := 0
	for i := 0; i < len(words); i++ {
		w := words[i]
		if len(w) > 2 && w[:2] == "--" {
			if i+1 == len(words) {
				return nil, fmt.Errorf("%s needs a value", w)
			}
			args[w[2:]] = words[i+1]
			i++
			continue
		}
		if next == len(tool.positional) {
			return nil, fmt.Errorf("unexpected %q\n%s", w, usage)
		}
		args[tool.positional[next]] = w
		next++
	}
	return args, nil
}
