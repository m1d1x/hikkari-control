// Package entrybench is the wire between a vantage and the hub for a bench of
// a relay build (roadmap 42 П6.14 Ф3): the routes the vantage's tunnelled
// traffic talks to and the summary it reports. Dependency-free on purpose —
// cmd/entry-prober runs on a small RU box and must not link the hub.
package entrybench

// Base is where the hub serves the bench routes (internal/entry bench_api.go).
const Base = "/api/v1/internal/entry-bench"

// Result is one run as the vantage measured it. Times in milliseconds,
// rates in megabits per second.
type Result struct {
	DurationS float64 `json:"durationS"`

	// Chat: small request/response exchanges on a held connection.
	ChatN        int     `json:"chatN"`
	ChatErrors   int     `json:"chatErrors"`
	ChatRTTP50Ms float64 `json:"chatRttP50Ms"`
	ChatRTTP95Ms float64 `json:"chatRttP95Ms"`

	// Media: photo-sized downloads and uploads, each timed whole.
	MediaN      int     `json:"mediaN"`
	MediaErrors int     `json:"mediaErrors"`
	DownMbps    float64 `json:"downMbps"` // median over the downloads
	UpMbps      float64 `json:"upMbps"`   // median over the uploads
	BytesDown   int64   `json:"bytesDown"`
	BytesUp     int64   `json:"bytesUp"`

	// Voice-like: a frame every 20 ms, echoed by the hub.
	VoiceSent     int     `json:"voiceSent"`
	VoiceLost     int     `json:"voiceLost"` // never echoed
	VoiceLate     int     `json:"voiceLate"` // echoed after VoiceLateMs
	VoiceRTTP50Ms float64 `json:"voiceRttP50Ms"`
	VoiceJitterMs float64 `json:"voiceJitterMs"` // mean |Δ rtt| between frames
	VoiceDrops    int     `json:"voiceDrops"`    // the stream broke and was reopened

	// Drops is every exchange the path broke: chat and media errors plus voice
	// stream breaks. The rollout wants zero.
	Drops int `json:"drops"`
	// Error: the run could not happen at all (no tunnel, the hub unreachable);
	// nothing above means anything then.
	Error string `json:"error,omitempty"`

	// Control is the run through the control relay, taken at the same time
	// when the bench named one (the task's second relay): the same path
	// drift, without the new build.
	Control *Result `json:"control,omitempty"`
}

// VoiceLateMs is when an echoed voice frame counts as late: past it, a jitter
// buffer would have played silence.
const VoiceLateMs = 300
