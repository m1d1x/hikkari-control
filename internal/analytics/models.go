package analytics

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Event is a validated analytics event ready for ClickHouse insertion.
type Event struct {
	UserID     uuid.UUID
	SessionID  uuid.UUID
	EventType  string
	Properties string // JSON string
	CreatedAt  time.Time
}

// IncomingEvent is the client-submitted event format.
type IncomingEvent struct {
	EventType  string         `json:"event_type"`
	Timestamp  string         `json:"timestamp"`
	SessionID  string         `json:"session_id"`
	Properties map[string]any `json:"properties"`
}

// LogRecord is a validated application log record ready for ClickHouse insertion.
type LogRecord struct {
	Timestamp time.Time
	Level     string
	Source    string // "frontend" | "backend"
	UserID    uuid.UUID
	SessionID string
	Category  string
	Message   string
	Context   string // JSON-encoded
	ServerID  string

	TraceID     string
	RequestID   string
	DurationMs  uint32
	Stacktrace  string
	Fingerprint string
	File        string
	Line        uint32
	ReleaseSHA  string
}

// SpanRecord is a single distributed-tracing span ready for ClickHouse insertion.
// Status maps to the Enum8 column: "ok" or "error".
type SpanRecord struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Service      string
	StartedAt    time.Time
	DurationMs   uint32
	Status       string // "ok" | "error"
	UserID       uuid.UUID
	Attrs        string // JSON-encoded
}

// ProbeRecord is one synthetic-network sample taken by cmd/sfu-prober from a
// known source region against a known target SFU node. Stored in the
// `synthetic_probes` ClickHouse table; used by the admin SFU dashboards to
// render uptime + packet-loss timeseries per (target, source) pair.
//
// Multiple packets are sent per sample, then aggregated into a single row:
//
//	PacketsSent  — how many probe packets were sent in this sample window
//	PacketsRecv  — how many we got responses for (PacketsSent - lost)
//	Success      — 1 if at least one packet came back, else 0 (uptime signal)
//	RTTMs        — mean round-trip time over received responses
//	JitterMs     — stddev of RTT across received responses
//	PacketLossPct — (1 - PacketsRecv/PacketsSent) * 100
type ProbeRecord struct {
	Timestamp     time.Time
	SourceRegion  string // e.g. "ru-msk", "eu-nl"
	TargetRegion  string
	ProbeType     string // "udp" | "tcp" | "tls"
	RTTMs         float32
	JitterMs      float32
	PacketLossPct float32
	Success       uint8
	PacketsSent   uint16
	PacketsRecv   uint16
}

// EntryProbeRecord is one Reality-relay liveness sample taken by
// cmd/entry-prober from an RU vantage against a single entry relay. Stored in
// the `entry_probes` ClickHouse table; feeds the plan-42 П6.7 censor-score
// dataset and the admin entry dashboard's "latest verdict per relay".
//
// It records the three signals of the plan-42 measurement methodology (§6),
// so the server can tell the CAUSE of a failure, not just the symptom:
//
//	RealityOK — S1: a request to the gateway /healthz succeeds THROUGH the
//	            relay's Reality tunnel (http 200). The liveness signal.
//	K1BareTLS — a plain TLS handshake straight to the relay IP:443 with the
//	            cover SNI, no Reality. Says whether the IP itself is reachable
//	            on 443 at all from the vantage.
//	K2Control — a plain TLS handshake to a known-allowed site (a different
//	            host/IP). The network-is-up control: if this fails the vantage
//	            itself is cut off and the other two signals are unreadable.
//
// Verdict is the §6 matrix applied to (RealityOK, K1BareTLS, K2Control):
//
//	clean        — Reality lives, K1 and K2 up (hypothesis H1 holds)
//	ip_banned    — Reality and K1 down, K2 up: the relay IP is blocked, Reality
//	               is blameless (swap the IP)
//	fingerprint  — Reality down but K1 up: the Reality fingerprint was detected
//	               (a real Reality failure — inspect uTLS/config)
//	reality_wins — Reality up where bare TLS (K1) is already dead: the best
//	               outcome, direct proof the obfuscation earns its keep
//	network_down — K2 down: the vantage has no clean path out, nothing readable
//	domain_blocked — an XHTTP relay (П4в): the address answers TLS, the
//	               relay's domain as SNI does not — the name is cut (swap the
//	               domain, keep the machine)
//	unknown      — not yet classified
type EntryProbeRecord struct {
	Timestamp    time.Time
	SourceRegion string // vantage label, e.g. "ru-spb"
	RelayName    string // edge registry name, <box>-<n>, e.g. "ams1-1"; no address — it is the secret
	RawTCP       uint8  // 1 if TCP:443 connect succeeded
	RealityOK    uint8  // 1 if the tunnel /healthz returned 200
	RealityRTTMs float32
	RealityHTTP  uint16 // http status code seen through the tunnel (0 on no response)
	K1BareTLS    uint8  // 1 if bare TLS to the relay IP (cover SNI) handshook
	K2Control    uint8  // 1 if TLS to the allowed-site control handshook
	Cover        string // the cover SNI used for K1
	Verdict      string // see the doc comment's §6 matrix
	Detail       string // free-text note, truncated to 256 chars on write
	// XUDPGot / XUDPSent: the voice door of a stream relay that also carries
	// calls — datagrams returned out of datagrams sent. Their own columns and
	// never a word in Verdict: the verdict is about the stream path, and
	// folding the door into it made a stream-healthy relay read `unknown`
	// (П9.7, cmd/entry-prober/voice.go).
	XUDPGot  uint16
	XUDPSent uint16
}

// EntryReachRecord is one client's attempt to reach the service (plan 42
// П9.9), as the client's own carrier race ended it. Stored in `entry_reach`.
//
// The counterpart of EntryProbeRecord: that one is measured from a vantage of
// ours, this one from the hands of people behind the censor — including the
// attempts that never connected, which no measurement of ours can see. It
// decides nothing; it is the dataset the console reads and the playbooks of
// П9.5 are judged against.
//
// No address and no account id. `Account` is the hub's keyed pseudonym
// (internal/entry: reachPseudonym), so distinct people can be counted and the
// rows still name nobody.
type EntryReachRecord struct {
	Timestamp time.Time
	Account   string // 32-char hex pseudonym, never an account id
	Outcome   string // direct | relay | failed
	RelayName string // the winning relay, empty unless Outcome is "relay"
	// RelaysTried counts the relays dialled in this attempt, winner included;
	// 0 when direct won without the tunnel racing.
	RelaysTried uint8
	ConnectMs   uint32 // end to end, the wait the person sat through
	RTTMs       uint32 // over the link that won; 0 when unmeasured
}

// EntryRelayStatRecord is one relay's self-report (plan 42 П9.3), written
// every half minute by the edge on the box. Stored in `entry_relay_stats`.
//
// Counters are totals since that edge process started, not deltas: a reader
// differences consecutive rows, and skips the pair where UptimeS fell — that
// is a restart, not a negative amount of traffic.
//
// No client identity of any kind, by construction: an edge keeps no session
// list, so there is nothing here to tell you who was on the box.
type EntryRelayStatRecord struct {
	Timestamp time.Time
	RelayName string
	Proto     string // resolved by the hub from the box, never claimed by the relay
	UptimeS   uint32
	BytesUp   uint64
	BytesDown uint64
	Conns     uint64
	CPUPct    float32
	MemMB     uint32
	Version   string
}

// EntryUserTrafficRecord is what one account's message socket carried since
// the gateway last flushed it (plan 42 П9.9), and through which box it came.
// Stored in `entry_user_traffic`.
//
// Account is the same keyed pseudonym as entry_reach's, never the account id;
// no address travels with the row — the hub resolved it to a box name and
// dropped it.
type EntryUserTrafficRecord struct {
	Timestamp time.Time
	Account   string // HMAC pseudonym, 32 hex chars
	Box       string // empty = the socket reached the gateway directly
	BytesUp   uint64
	BytesDown uint64
}

// EntryBenchRecord is one bench run of a relay build (plan 42 П6.14 Ф3):
// messenger-shaped traffic from an RU vantage through one relay of a box to
// the hub, before or after a new nekko-edge went on it. Stored in
// `entry_benches`; the rollout itself decides from Postgres, this is the
// dataset. No address, like entry_probes: the box and relay names say where.
type EntryBenchRecord struct {
	Timestamp    time.Time
	TaskID       string
	SourceRegion string
	RelayName    string
	Box          string
	Arch         string
	EdgeSHA      string
	Phase        string // before | after | manual
	DurationS    float32
	ChatN        uint32
	ChatErrors   uint32
	ChatRTTP50Ms float32
	ChatRTTP95Ms float32
	MediaN       uint32
	MediaErrors  uint32
	DownMbps     float32
	UpMbps       float32
	BytesDown    uint64
	BytesUp      uint64
	VoiceSent    uint32
	VoiceLost    uint32
	VoiceLate    uint32
	VoiceRTTP50  float32
	VoiceJitter  float32
	VoiceDrops   uint32
	Drops        uint32
	Error        string
}

// WebRTCStatRecord is one client-side getStats() sample submitted by the
// browser/desktop client every ~10s. Stored in the `webrtc_stats` ClickHouse
// table; the admin SFU dashboards aggregate this into p50/p95/p99 timeseries.
//
// AudioLevel comes from the inbound-rtp report when the publisher emits
// `audioLevel` (0..1). Useful to distinguish "silent room" from "broken RTP".
type WebRTCStatRecord struct {
	Timestamp       time.Time
	SessionID       string
	UserID          uuid.UUID
	NodeRegion      string // SFU shard the client is connected to
	ClientKind      string // "browser" | "desktop"
	RTTMs           float32
	JitterMs        float32
	PacketLossPct   float32
	PacketsLost     uint32
	PacketsReceived uint32
	NackCount       uint32
	AudioLevel      float32
	BytesPerSec     uint32
}

// HealthProbeRecord is one health-check sample taken against a backing service
// (Postgres/Redis/CH/S3/SFU) or a public HTTP endpoint. Stored in the
// `health_probes` table; the admin Health tab renders 24h sparklines per
// (target, kind).
//
// Kind separates the two probe layers:
//
//	"internal" — direct connection to the service (e.g. SELECT 1 against
//	             Postgres, PING against Redis, gRPC Health against SFU).
//	             Says nothing about reachability from outside the host.
//	"external" — HTTP(S) GET against a public URL, i.e. what an end user
//	             sees. ⚠️ Nothing writes these rows: the external prober went
//	             with the synthetic runner (audit В15/Б27).
//
// Status mirrors HealthPoint.Status in internal/admin (ok | error |
// degraded | timeout | unknown).
type HealthProbeRecord struct {
	Timestamp time.Time
	Target    string // service name or hostname (lowercase, stable)
	Kind      string // "internal" | "external"
	Status    string // "ok" | "error" | "degraded" | "timeout" | "unknown"
	LatencyMs uint32
	HTTPCode  uint16 // 0 for non-HTTP probes
	Error     string // empty on success, truncated to 256 chars
}

// CSPViolationRecord is one Content-Security-Policy violation report posted
// by a browser to /api/csp-report. Stored in the `csp_violations` ClickHouse
// table. Fields mirror the legacy CSP report-uri JSON (`{"csp-report": ...}`)
// — modern Reporting API shape can be flattened into the same columns when
// we wire it up later.
type CSPViolationRecord struct {
	Timestamp          time.Time
	IP                 string
	DocumentURI        string
	Referrer           string
	ViolatedDirective  string
	EffectiveDirective string
	BlockedURI         string
	SourceFile         string
	LineNumber         uint32
	ColumnNumber       uint32
	Disposition        string // "report" | "enforce"
	Raw                string // full JSON body for debugging
}

// Validate checks required fields.
// Event types are not filtered — the EventBus emits domain-prefixed events
// (e.g. "chat:message_sent", "discover:scroll") and we want to capture all of them.
func (e *IncomingEvent) Validate() error {
	if e.EventType == "" {
		return fmt.Errorf("event_type required")
	}
	if e.SessionID == "" {
		return fmt.Errorf("session_id required")
	}
	return nil
}
