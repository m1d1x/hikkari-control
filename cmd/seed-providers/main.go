package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/entrywire"
)

func main() {
	dbURL := flag.String("db", "", "PostgreSQL database URL (defaults to DATABASE_URL)")
	contentKey := flag.String("key", "", "NEKKO_CONTENT_KEY (defaults to env)")
	flag.Parse()

	if *dbURL == "" {
		*dbURL = os.Getenv("DATABASE_URL")
	}
	if *dbURL == "" {
		*dbURL = "postgres://hc:hc@127.0.0.1:55432/hub_hikkari?sslmode=disable"
	}
	if *contentKey == "" {
		*contentKey = os.Getenv("NEKKO_CONTENT_KEY")
	}
	if *contentKey == "" {
		*contentKey = "KJ5DHFecbs+UNKszBfhDpWjxlBx/E/d5UYuiK4JHvwo="
	}

	_ = os.Setenv("NEKKO_CONTENT_KEY", *contentKey)
	if err := cryptobox.Configure(nil); err != nil {
		log.Fatalf("cryptobox configure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("ping db: %v", err)
	}

	svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
	if err != nil {
		log.Fatalf("new entry service: %v", err)
	}
	svc.SetProviderConnector(entrywire.Providers())

	// 1. Seed VDSina
	fmt.Println("==> Seeding VDSina...")
	vdsinaCreds := map[string]string{
		"api_token": "f6724032c7cc6458800a72cea59bf4501bf2d84b09bd0192401c3d05ff3b821a",
	}
	vdsinaView, err := svc.SaveProvider(ctx, "vdsina", entry.ProviderUpdate{
		Enabled:       true,
		Credentials:   vdsinaCreds,
		DefaultRegion: "4",
		DefaultPlan:   "36",
	}, "")
	if err != nil {
		log.Fatalf("save vdsina: %v", err)
	}
	fmt.Printf("[+] VDSina saved: enabled=%v, region=%s, plan=%s, checkOK=%v\n",
		vdsinaView.Enabled, vdsinaView.DefaultRegion, vdsinaView.DefaultPlan, vdsinaView.Check != nil && vdsinaView.Check.OK)

	// 2. Seed OpenStack
	fmt.Println("==> Seeding OpenStack...")
	osCreds := map[string]string{
		"auth_url":   "https://os-api.hostvds.com/identity",
		"username":   "hostvds-e5eea76c-496c-4111-987f-1d6e7d5dbb85",
		"password":   "8oS3ha8Ihr2XT6t6Z8ZlphT3V6s",
		"region":     "eu-north2",
		"project":    "hostvds-e5eea76c-496c-4111-987f-1d6e7d5dbb85",
		"network_id": "3f8d2236-54bf-4e64-b43f-33319a5b2e47",
	}
	osView, err := svc.SaveProvider(ctx, "openstack", entry.ProviderUpdate{
		Enabled:       true,
		Credentials:   osCreds,
		DefaultRegion: "eu-north2",
		DefaultPlan:   "55a40993-53c3-4236-b785-8772b9265f4d",
	}, "")
	if err != nil {
		log.Fatalf("save openstack: %v", err)
	}
	fmt.Printf("[+] OpenStack saved: enabled=%v, region=%s, plan=%s, checkOK=%v\n",
		osView.Enabled, osView.DefaultRegion, osView.DefaultPlan, osView.Check != nil && osView.Check.OK)

	fmt.Println("\n[SUCCESS] Both providers successfully saved and enabled in entry_providers!")
}
