package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

func runTrunkPointTLS(args []string) error {
	if err := cryptobox.Configure(os.Getenv); err != nil {
		return fmt.Errorf("content key: %w", err)
	}
	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()
	svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
	if err != nil {
		return err
	}
	if args[0] == "border-point-tls" {
		if len(args) == 3 && args[2] == "off" {
			return svc.ClearCorePointTLS(ctx, args[1])
		}
		if len(args) != 5 || (args[3] != "legacy" && args[3] != "strict") {
			return fmt.Errorf("usage: entry-box border-point-tls <core> <relay-port> legacy|strict <point>=<domain>,... | <core> off")
		}
		port, err := strconv.Atoi(args[2])
		if err != nil {
			return err
		}
		names := map[string]string{}
		for _, spec := range strings.Split(args[4], ",") {
			point, name, ok := strings.Cut(spec, "=")
			if !ok || point == "" || name == "" || names[point] != "" {
				return fmt.Errorf("invalid or repeated point binding %q", spec)
			}
			names[point] = strings.ToLower(name)
		}
		return svc.SetCorePointTLS(ctx, args[1], port, args[3] == "legacy", names)
	}
	if len(args) != 4 || (args[2] != "stand" && args[2] != "people") {
		return fmt.Errorf("usage: entry-box trunk-point-tls <entry> stand|people <point>,...|off")
	}
	var points []string
	if args[3] != "off" {
		points = strings.Split(args[3], ",")
	}
	return newProvisioner(svc).SetTrunkPointTLS(ctx, args[1], args[2] == "stand", points)
}
