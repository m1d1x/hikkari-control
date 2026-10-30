package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SessionData holds enriched session data written to the sessions table.
type SessionData struct {
	SessionID      uuid.UUID
	UserID         uuid.UUID
	StartedAt      time.Time
	Referrer       string
	UTMSource      string
	UTMMedium      string
	UTMCampaign    string
	IPAddress      string
	CountryCode    string
	CountryName    string
	Region         string
	City           string
	Browser        string
	BrowserVersion string
	OS             string
	OSVersion      string
	DeviceType     string

	// Fraud detection fields (Phase 5)
	ASNNumber       uint32
	ASNOrg          string
	IsDatacenter    uint8
	IsVPN           uint8
	IsProxy         uint8
	IsTor           uint8
	IsHosting       uint8
	PoWSolved       uint8
	PoWTimeMs       uint32
	FpHash          string
	RiskScore       uint8
	BehaviorSignals string // JSON-encoded behavior data
}

// InsertSession writes a single session record to ClickHouse.
func (s *Store) InsertSession(ctx context.Context, session SessionData) error {
	batch, err := s.conn.PrepareBatch(ctx, `
		INSERT INTO sessions (
			session_id, user_id, started_at,
			referrer, utm_source, utm_medium, utm_campaign,
			ip_address, country_code, country_name, region, city,
			browser, browser_version, os, os_version, device_type,
			asn_number, asn_org, is_datacenter, is_vpn, is_proxy, is_tor, is_hosting,
			pow_solved, pow_time_ms, fp_hash, risk_score, behavior_signals
		)`)
	if err != nil {
		return fmt.Errorf("prepare session batch: %w", err)
	}

	err = batch.Append(
		session.SessionID,
		session.UserID,
		session.StartedAt,
		session.Referrer,
		session.UTMSource,
		session.UTMMedium,
		session.UTMCampaign,
		session.IPAddress,
		session.CountryCode,
		session.CountryName,
		session.Region,
		session.City,
		session.Browser,
		session.BrowserVersion,
		session.OS,
		session.OSVersion,
		session.DeviceType,
		session.ASNNumber,
		session.ASNOrg,
		session.IsDatacenter,
		session.IsVPN,
		session.IsProxy,
		session.IsTor,
		session.IsHosting,
		session.PoWSolved,
		session.PoWTimeMs,
		session.FpHash,
		session.RiskScore,
		session.BehaviorSignals,
	)
	if err != nil {
		return fmt.Errorf("append session: %w", err)
	}

	return batch.Send()
}
