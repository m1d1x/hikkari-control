package downdetector

// External is the state of our own outside probe of a server.
type External string

const (
	ExternalUp      External = "up"
	ExternalDown    External = "down"
	ExternalUnknown External = "unknown"
)

// Verdict is what the client shows when the server does not work for it.
type Verdict string

const (
	VerdictServerDown   Verdict = "server_down"
	VerdictISPBlock     Verdict = "isp_block"
	VerdictCountryBlock Verdict = "country_block"
	VerdictLocal        Verdict = "local"
	VerdictUnknown      Verdict = "unknown"
)

// share is the fraction of tunnel probes that decides a group either way.
const share = 0.7

func (g Group) empty() bool { return g.Installs == 0 }

func (g Group) alive(k int) bool {
	n := g.OK + g.Fail
	return g.Installs >= k && n > 0 && float64(g.OK) >= share*float64(n)
}

func (g Group) dead(k int) bool {
	n := g.OK + g.Fail
	return g.Installs >= k && n > 0 && float64(g.Fail) >= share*float64(n)
}

// Decide is the verdict for a user whose server does not work, given the
// external probe and the tunnel-probe groups over the last 15 minutes.
func Decide(ext External, sameASN, sameCountry, abroad Group, k int) Verdict {
	if ext == ExternalDown {
		return VerdictServerDown
	}
	if ext == ExternalUnknown {
		withData, allDead := 0, true
		for _, g := range []Group{sameASN, sameCountry, abroad} {
			if g.empty() {
				continue
			}
			withData++
			allDead = allDead && g.dead(k)
		}
		if withData > 0 && allDead {
			return VerdictServerDown
		}
	}
	switch {
	case sameASN.dead(k) && (sameCountry.dead(k) || sameCountry.empty()) && abroad.alive(k):
		return VerdictCountryBlock
	case sameASN.dead(k) && sameCountry.alive(k):
		return VerdictISPBlock
	case sameASN.alive(k):
		return VerdictLocal
	}
	return VerdictUnknown
}
