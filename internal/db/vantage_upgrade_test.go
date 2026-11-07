package db

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVantageConfirmationUpgradePreservesDeployedSchema(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(testDatabaseEnv))
	if dsn == "" {
		t.Skip("no test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, kind := range []string{"text", "text[]"} {
		t.Run(kind, func(t *testing.T) {
			name := fmt.Sprintf("hikkari_vantage_upgrade_%d", time.Now().UnixNano())
			if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				if _, e := admin.Exec(cleanup, "DROP DATABASE "+name+" WITH (FORCE)"); e != nil {
					t.Error(e)
				}
			}()
			pool, e := pgxpool.New(ctx, replaceDBName(dsn, name))
			if e != nil {
				t.Fatal(e)
			}
			defer pool.Close()
			ddl := `CREATE TABLE entry_probe_tasks(id int PRIMARY KEY,not_by ` + kind + `); CREATE TABLE entry_suspects(id int PRIMARY KEY);`
			if _, e = pool.Exec(ctx, ddl); e != nil {
				t.Fatal(e)
			}
			if kind == "text" {
				_, e = pool.Exec(ctx, `INSERT INTO entry_probe_tasks VALUES (1,'ru-spb'),(2,NULL),(3,'')`)
			} else {
				_, e = pool.Exec(ctx, `INSERT INTO entry_probe_tasks VALUES (1,ARRAY['ru-spb','ru-eurodir']),(2,NULL),(3,'{}')`)
			}
			if e != nil {
				t.Fatal(e)
			}
			sql, e := migrationsFS.ReadFile("migrations/035_vantage_confirmation_upgrade.sql")
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 2; i++ {
				if _, e = pool.Exec(ctx, string(sql)); e != nil {
					t.Fatal(e)
				}
			}
			var excluded []string
			if e = pool.QueryRow(ctx, `SELECT not_by FROM entry_probe_tasks WHERE id=1`).Scan(&excluded); e != nil {
				t.Fatal(e)
			}
			want := []string{"ru-spb"}
			if kind == "text[]" {
				want = append(want, "ru-eurodir")
			}
			if !reflect.DeepEqual(excluded, want) {
				t.Fatalf("exclusions lost: %v want %v", excluded, want)
			}
			var empty bool
			if e = pool.QueryRow(ctx, `SELECT not_by IS NULL FROM entry_probe_tasks WHERE id=2`).Scan(&empty); e != nil || !empty {
				t.Fatal("NULL changed", e)
			}
			var testers bool
			if e = pool.QueryRow(ctx, `INSERT INTO entry_suspects(id) VALUES (1) RETURNING testers`).Scan(&testers); e != nil || testers {
				t.Fatal("tester evidence default changed", e)
			}
		})
	}
}
