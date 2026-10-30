package downdetector

import "time"

// FleetVerdict evaluates only k-anonymous tunnel groups in completed report
// windows. The outside TCP probe alone can report an outage, never DPI health.
func (s *Store) FleetVerdict(id string, ext External, now, since time.Time, k int) string {
	if ext == ExternalDown {
		return string(VerdictServerDown)
	}
	type audience struct {
		asn     uint32
		country string
	}
	audiences := map[audience]bool{}
	cur := alignedStart(now)
	s.mu.Lock()
	for start, b := range s.buckets {
		if start >= cur || start < cur-StatusWindows*WindowSec || (!since.IsZero() && start < since.Unix()) {
			continue
		}
		for key := range b.cells {
			if key.Server == id && key.Src == SrcTunnel && key.Country != "" {
				audiences[audience{key.ASN, key.Country}] = true
			}
		}
	}
	s.mu.Unlock()
	result := string(VerdictUnknown)
	for a := range audiences {
		same, country, abroad := s.GroupsSince(id, a.asn, a.country, now, since)
		switch Decide(ext, same, country, abroad, k) {
		case VerdictServerDown:
			return string(VerdictServerDown)
		case VerdictCountryBlock:
			result = string(VerdictCountryBlock)
		case VerdictISPBlock:
			if result != string(VerdictCountryBlock) {
				result = string(VerdictISPBlock)
			}
		case VerdictLocal:
			if result == string(VerdictUnknown) && ext == ExternalUp {
				result = "healthy"
			}
		}
	}
	return result
}
