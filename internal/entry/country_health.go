package entry

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/m1d1x/hikkari-control/internal/applog"
)

// WatchCountryHealth reads the entry's heartbeat separately from rollout jobs.
// The subscription status expires automatically when either side goes away.
func (p *Provisioner) WatchCountryHealth(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		rows, err := p.svc.pool.Query(ctx, `SELECT entry_box FROM entry_country_egress WHERE applied_sha <> ''`)
		if err == nil {
			var boxes []string
			boxes, err = pgx.CollectRows(rows, pgx.RowTo[string])
			for _, box := range boxes {
				p.readCountryHealth(ctx, box)
			}
		}
		if err != nil && ctx.Err() == nil {
			applog.Error(ctx, "entry", "country health", applog.F("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Provisioner) readCountryHealth(ctx context.Context, box string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return
	}
	defer r.Close()
	out, err := r.Run(ctx, "cat /run/"+countryGatewayUnit.name+"/health", nil)
	if err != nil {
		return
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return
	}
	stamp, err := strconv.ParseInt(fields[0], 10, 64)
	at := time.Unix(stamp, 0)
	if err != nil || time.Since(at) > 15*time.Second || time.Until(at) > 5*time.Second {
		return
	}
	down := fields[1]
	if down != "NL" && down != "DE" && down != "none" {
		return
	}
	if _, err := p.svc.pool.Exec(ctx, `UPDATE entry_country_egress SET down_country = $2, health_at = $3
        WHERE entry_box = $1 AND (health_at IS NULL OR health_at < $3)`, box, down, at); err != nil && ctx.Err() == nil {
		applog.Error(ctx, "entry", "store country health", applog.F("box", box), applog.F("err", err.Error()))
	}
}
