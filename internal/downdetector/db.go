package downdetector

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB is the on-disk dataset: hourly aggregates only, never raw reports or
// install ids.
type DB struct{ sql *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS rollup_hourly (
	hour        INTEGER NOT NULL,
	server_id   TEXT    NOT NULL,
	proto       TEXT    NOT NULL,
	src         TEXT    NOT NULL,
	asn         INTEGER NOT NULL,
	country     TEXT    NOT NULL,
	region      TEXT    NOT NULL,
	ok          INTEGER NOT NULL,
	fail        INTEGER NOT NULL,
	rtt_p50_avg INTEGER NOT NULL,
	installs    INTEGER NOT NULL,
	PRIMARY KEY (hour, server_id, proto, src, asn, country, region)
);
CREATE INDEX IF NOT EXISTS rollup_hourly_hour ON rollup_hourly(hour);
-- Hours already rolled up. A late report for one of them still counts for
-- live status but is not written a second time.
CREATE TABLE IF NOT EXISTS rollup_done (hour INTEGER PRIMARY KEY);
`

func OpenDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &DB{db}, nil
}

func (d *DB) Close() error { return d.sql.Close() }

// WriteHour stores one hour's rows and marks the hour done, atomically.
// An hour that is already done is skipped.
func (d *DB) WriteHour(ctx context.Context, hour int64, rows []RollupRow) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO rollup_done(hour) VALUES (?)`, hour)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO rollup_hourly
		(hour, server_id, proto, src, asn, country, region, ok, fail, rtt_p50_avg, installs)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Hour, r.Server, r.Proto, r.Src, r.ASN, r.Country, r.Region,
			r.OK, r.Fail, r.RttP50Avg, r.Installs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OverviewRow is provider × region × proto × hour, summed over servers.
type OverviewRow struct {
	Hour     int64  `json:"hour"`
	ASN      uint32 `json:"asn"`
	Country  string `json:"country"`
	Region   string `json:"region"`
	Proto    string `json:"proto"`
	Src      string `json:"src"`
	Servers  int    `json:"servers"`
	OK       int    `json:"ok"`
	Fail     int    `json:"fail"`
	RttP50   int    `json:"rtt_p50_avg"`
	Installs int    `json:"installs_max"` // largest per-server count; installs do not add up across servers
}

func (d *DB) Overview(ctx context.Context, since int64) ([]OverviewRow, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT hour, asn, country, region, proto, src,
		       COUNT(DISTINCT server_id), SUM(ok), SUM(fail),
		       CAST(AVG(rtt_p50_avg) AS INTEGER), MAX(installs)
		FROM rollup_hourly WHERE hour >= ?
		GROUP BY hour, asn, country, region, proto, src
		ORDER BY hour, asn, country, region, proto, src`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OverviewRow{}
	for rows.Next() {
		var r OverviewRow
		if err := rows.Scan(&r.Hour, &r.ASN, &r.Country, &r.Region, &r.Proto, &r.Src,
			&r.Servers, &r.OK, &r.Fail, &r.RttP50, &r.Installs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
