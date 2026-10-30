package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SFU metrics carry a constant `region` label so a Prometheus query can group
// across geo-shards without depending on scrape-job labels. Each SFU process
// owns one region; cardinality stays at 1 per binary.
//
// Counters use the _total suffix per Prometheus convention. The two "live"
// gauges intentionally drop _total — they describe current state, not a
// monotonically-increasing aggregate.
var (
	sfuRoomsActive prometheus.Gauge
	sfuPeersActive prometheus.Gauge

	// Pre-resolved per-kind counters. Looking up labels via WithLabelValues on
	// every RTP packet would be wasteful — voice rooms push 50 audio packets
	// per peer per second, screen video pushes far more.
	sfuPacketsAudio       prometheus.Counter
	sfuPacketsScreenVideo prometheus.Counter
	sfuPacketsScreenAudio prometheus.Counter

	// Host-level VPS gauges. Sampled by cmd/sfu via gopsutil so the admin
	// dashboard can show per-region VPS load (CPU/RAM) without deploying a
	// separate node_exporter.
	sfuHostCPUPercent prometheus.Gauge
	sfuHostMemUsed    prometheus.Gauge
	sfuHostMemTotal   prometheus.Gauge
	sfuHostMemPercent prometheus.Gauge

	// ── Signaling rate-limit (L1-L4) ────────────────────────────────────
	// Pre-resolved per-(layer, outcome) counters. The signaling hot path
	// hits these on every WS message — paying for label lookup per call
	// would be wasteful at the rates we care about.
	//
	// Outcomes:
	//   allowed     — limiter said yes
	//   violations  — limiter said no (always counted, even in observe)
	//   dropped     — message rejected because enforce mode was on
	sfuSigL1AllowedGen    prometheus.Counter
	sfuSigL1AllowedICE    prometheus.Counter
	sfuSigL1AllowedExempt prometheus.Counter
	sfuSigL1ViolationsGen prometheus.Counter
	sfuSigL1ViolationsICE prometheus.Counter
	sfuSigL1DroppedGen    prometheus.Counter
	sfuSigL1DroppedICE    prometheus.Counter

	sfuSigL2Violations prometheus.Counter
	sfuSigL2Dropped    prometheus.Counter

	sfuSigL3Allowed     prometheus.Counter
	sfuSigL3BucketHits  prometheus.Counter
	sfuSigL3BackoffHits prometheus.Counter
	sfuSigL3Dropped     prometheus.Counter

	sfuSigL4Allowed     prometheus.Counter
	sfuSigL4RoomHits    prometheus.Counter
	sfuSigL4AccountHits prometheus.Counter
	sfuSigL4Dropped     prometheus.Counter

	sfuSigEnforceMode prometheus.Gauge

	sfuOnce sync.Once
)

// InitSFU registers the SFU voice metrics with `region` as a constant label.
// Safe to call multiple times — only the first call wins. Pass an empty string
// to fall back to "default" so unconfigured local builds still scrape cleanly.
func InitSFU(region, nodeID string) {
	sfuOnce.Do(func() {
		if region == "" {
			region = "default"
		}
		if nodeID == "" {
			nodeID = region
		}
		// Two labels, not one. `region` alone was the shard's whole identity
		// until task 57.2, so two shards in one region produced the SAME
		// series and silently fought over it — one shard's gauge overwriting
		// the other's. Dashboards keep grouping by region; node is what makes
		// the members of that group distinguishable.
		constLabels := prometheus.Labels{"region": region, "node": nodeID}

		sfuRoomsActive = promauto.NewGauge(prometheus.GaugeOpts{
			Namespace:   "nekko",
			Name:        "sfu_rooms_active",
			Help:        "Number of active SFU voice rooms on this shard.",
			ConstLabels: constLabels,
		})
		sfuPeersActive = promauto.NewGauge(prometheus.GaugeOpts{
			Namespace:   "nekko",
			Name:        "sfu_peers_active",
			Help:        "Number of peers currently joined to any voice room on this shard.",
			ConstLabels: constLabels,
		})

		packets := promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace:   "nekko",
			Name:        "sfu_packets_forwarded_total",
			Help:        "Total RTP packets forwarded by this SFU shard, by kind.",
			ConstLabels: constLabels,
		}, []string{"kind"})

		sfuPacketsAudio = packets.WithLabelValues("audio")
		sfuPacketsScreenVideo = packets.WithLabelValues("screen_video")
		sfuPacketsScreenAudio = packets.WithLabelValues("screen_audio")

		sfuHostCPUPercent = promauto.NewGauge(prometheus.GaugeOpts{
			Namespace:   "nekko",
			Name:        "sfu_host_cpu_percent",
			Help:        "Host VPS CPU utilisation (0-100), sampled every 10s.",
			ConstLabels: constLabels,
		})
		sfuHostMemUsed = promauto.NewGauge(prometheus.GaugeOpts{
			Namespace:   "nekko",
			Name:        "sfu_host_mem_used_bytes",
			Help:        "Host VPS memory used, in bytes.",
			ConstLabels: constLabels,
		})
		sfuHostMemTotal = promauto.NewGauge(prometheus.GaugeOpts{
			Namespace:   "nekko",
			Name:        "sfu_host_mem_total_bytes",
			Help:        "Host VPS memory total, in bytes.",
			ConstLabels: constLabels,
		})
		sfuHostMemPercent = promauto.NewGauge(prometheus.GaugeOpts{
			Namespace:   "nekko",
			Name:        "sfu_host_mem_percent",
			Help:        "Host VPS memory utilisation (0-100).",
			ConstLabels: constLabels,
		})

		// ── Signaling rate-limit ────────────────────────────────────────
		// One CounterVec per layer with an `outcome` label (and a `lane`
		// label on layers that fan out by message category).
		sigL1 := promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace:   "nekko",
			Name:        "sfu_signaling_l1_total",
			Help:        "Per-WS signaling rate-limit decisions (L1), by lane and outcome.",
			ConstLabels: constLabels,
		}, []string{"lane", "outcome"})
		sfuSigL1AllowedGen = sigL1.WithLabelValues("general", "allowed")
		sfuSigL1AllowedICE = sigL1.WithLabelValues("ice", "allowed")
		sfuSigL1AllowedExempt = sigL1.WithLabelValues("exempt", "allowed")
		sfuSigL1ViolationsGen = sigL1.WithLabelValues("general", "violations")
		sfuSigL1ViolationsICE = sigL1.WithLabelValues("ice", "violations")
		sfuSigL1DroppedGen = sigL1.WithLabelValues("general", "dropped")
		sfuSigL1DroppedICE = sigL1.WithLabelValues("ice", "dropped")

		sigL2 := promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace:   "nekko",
			Name:        "sfu_signaling_l2_total",
			Help:        "Per-PC renegotiation cooldown decisions (L2), by outcome.",
			ConstLabels: constLabels,
		}, []string{"outcome"})
		sfuSigL2Violations = sigL2.WithLabelValues("violations")
		sfuSigL2Dropped = sigL2.WithLabelValues("dropped")

		sigL3 := promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace:   "nekko",
			Name:        "sfu_signaling_l3_total",
			Help:        "Per-account join/leave rate-limit decisions (L3), by outcome.",
			ConstLabels: constLabels,
		}, []string{"outcome"})
		sfuSigL3Allowed = sigL3.WithLabelValues("allowed")
		sfuSigL3BucketHits = sigL3.WithLabelValues("bucket_hits")
		sfuSigL3BackoffHits = sigL3.WithLabelValues("backoff_hits")
		sfuSigL3Dropped = sigL3.WithLabelValues("dropped")

		sigL4 := promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace:   "nekko",
			Name:        "sfu_signaling_l4_total",
			Help:        "Global signaling rate-limit decisions (L4), by outcome.",
			ConstLabels: constLabels,
		}, []string{"outcome"})
		sfuSigL4Allowed = sigL4.WithLabelValues("allowed")
		sfuSigL4RoomHits = sigL4.WithLabelValues("room_hits")
		sfuSigL4AccountHits = sigL4.WithLabelValues("account_hits")
		sfuSigL4Dropped = sigL4.WithLabelValues("dropped")

		sfuSigEnforceMode = promauto.NewGauge(prometheus.GaugeOpts{
			Namespace:   "nekko",
			Name:        "sfu_signaling_ratelimit_enforce_mode",
			Help:        "Whether signaling rate-limit enforcement is on (1) or observe-only (0).",
			ConstLabels: constLabels,
		})
	})
}

// SFUSetHostStats updates the host-level gauges. Called from cmd/sfu's
// background sampler; no-op if InitSFU was never called.
func SFUSetHostStats(cpuPercent, memUsed, memTotal, memPercent float64) {
	if sfuHostCPUPercent != nil {
		sfuHostCPUPercent.Set(cpuPercent)
	}
	if sfuHostMemUsed != nil {
		sfuHostMemUsed.Set(memUsed)
	}
	if sfuHostMemTotal != nil {
		sfuHostMemTotal.Set(memTotal)
	}
	if sfuHostMemPercent != nil {
		sfuHostMemPercent.Set(memPercent)
	}
}

// SFUSetRooms / SFUSetPeers are no-ops if InitSFU was never called — keeps
// tests and tools that link the package without bootstrapping metrics safe.
func SFUSetRooms(n int) {
	if sfuRoomsActive != nil {
		sfuRoomsActive.Set(float64(n))
	}
}

func SFUSetPeers(n int) {
	if sfuPeersActive != nil {
		sfuPeersActive.Set(float64(n))
	}
}

func SFUForwardedAudio() {
	if sfuPacketsAudio != nil {
		sfuPacketsAudio.Inc()
	}
}

func SFUForwardedScreenVideo() {
	if sfuPacketsScreenVideo != nil {
		sfuPacketsScreenVideo.Inc()
	}
}

func SFUForwardedScreenAudio() {
	if sfuPacketsScreenAudio != nil {
		sfuPacketsScreenAudio.Inc()
	}
}

// ── Signaling rate-limit emitters ───────────────────────────────────────────
// Each function is nil-safe so binaries that link this package without
// calling InitSFU still run. The rate-limit hot path calls these on every
// signaling message; the nil-check + Inc cost is negligible vs the channel
// of decisions we'd otherwise lose visibility on.

// SFUSignalingL1 records a per-WS rate-limit decision. lane ∈
// {"general","ice","exempt"}, outcome ∈ {"allowed","violations","dropped"}.
func SFUSignalingL1(lane, outcome string) {
	switch outcome {
	case "allowed":
		switch lane {
		case "general":
			if sfuSigL1AllowedGen != nil {
				sfuSigL1AllowedGen.Inc()
			}
		case "ice":
			if sfuSigL1AllowedICE != nil {
				sfuSigL1AllowedICE.Inc()
			}
		case "exempt":
			if sfuSigL1AllowedExempt != nil {
				sfuSigL1AllowedExempt.Inc()
			}
		}
	case "violations":
		switch lane {
		case "general":
			if sfuSigL1ViolationsGen != nil {
				sfuSigL1ViolationsGen.Inc()
			}
		case "ice":
			if sfuSigL1ViolationsICE != nil {
				sfuSigL1ViolationsICE.Inc()
			}
		}
	case "dropped":
		switch lane {
		case "general":
			if sfuSigL1DroppedGen != nil {
				sfuSigL1DroppedGen.Inc()
			}
		case "ice":
			if sfuSigL1DroppedICE != nil {
				sfuSigL1DroppedICE.Inc()
			}
		}
	}
}

// SFUSignalingL2 records a per-PC renegotiation cooldown decision.
// outcome ∈ {"violations","dropped"}.
func SFUSignalingL2(outcome string) {
	switch outcome {
	case "violations":
		if sfuSigL2Violations != nil {
			sfuSigL2Violations.Inc()
		}
	case "dropped":
		if sfuSigL2Dropped != nil {
			sfuSigL2Dropped.Inc()
		}
	}
}

// SFUSignalingL3 records a per-account join/leave decision.
// outcome ∈ {"allowed","bucket_hits","backoff_hits","dropped"}.
func SFUSignalingL3(outcome string) {
	switch outcome {
	case "allowed":
		if sfuSigL3Allowed != nil {
			sfuSigL3Allowed.Inc()
		}
	case "bucket_hits":
		if sfuSigL3BucketHits != nil {
			sfuSigL3BucketHits.Inc()
		}
	case "backoff_hits":
		if sfuSigL3BackoffHits != nil {
			sfuSigL3BackoffHits.Inc()
		}
	case "dropped":
		if sfuSigL3Dropped != nil {
			sfuSigL3Dropped.Inc()
		}
	}
}

// SFUSignalingL4 records a global signaling decision.
// outcome ∈ {"allowed","room_hits","account_hits","dropped"}.
func SFUSignalingL4(outcome string) {
	switch outcome {
	case "allowed":
		if sfuSigL4Allowed != nil {
			sfuSigL4Allowed.Inc()
		}
	case "room_hits":
		if sfuSigL4RoomHits != nil {
			sfuSigL4RoomHits.Inc()
		}
	case "account_hits":
		if sfuSigL4AccountHits != nil {
			sfuSigL4AccountHits.Inc()
		}
	case "dropped":
		if sfuSigL4Dropped != nil {
			sfuSigL4Dropped.Inc()
		}
	}
}

// SFUSignalingEnforceMode reflects the current enforce flag (0|1).
func SFUSignalingEnforceMode(on bool) {
	if sfuSigEnforceMode == nil {
		return
	}
	v := 0.0
	if on {
		v = 1
	}
	sfuSigEnforceMode.Set(v)
}
