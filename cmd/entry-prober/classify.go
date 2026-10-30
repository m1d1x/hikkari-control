package main

// Verdict names mirror the entry_probes Enum8 in internal/analytics.
const (
	verdictClean       = "clean"
	verdictIPBanned    = "ip_banned"
	verdictFingerprint = "fingerprint"
	verdictRealityWins = "reality_wins"
	verdictNetworkDown = "network_down"
	verdictUnknown     = "unknown"
	// verdictDomainBlocked: an XHTTP relay's address answers, its domain does
	// not — the censor cuts the name by SNI (П4в).
	verdictDomainBlocked = "domain_blocked"
)

// classify applies the plan-42 measurement methodology §6 control matrix to the
// three signals and names the CAUSE, not just the symptom:
//
//	reality — a request to the gateway /healthz succeeded THROUGH the relay's
//	          Reality tunnel (the liveness signal).
//	k3      — the tunnel carried a connection at all: the Reality handshake
//	          completed and the relay forwarded us, whatever came back (П9.4).
//	k1      — a bare TLS handshake straight to the relay IP:port with the cover
//	          SNI, no Reality. Says whether the IP is reachable on that port.
//	k2      — a bare TLS handshake to a known-allowed control site (a different
//	          host). The network-is-up control.
//
// The matrix:
//
//	reality k3 k1 k2 | verdict
//	------- -- -- -- | -------
//	   *     *  *  ✗ | network_down  — the vantage has no clean path out at all;
//	                 |                 the other signals are unreadable, so we
//	                 |                 refuse to blame the relay.
//	   ✓     *  ✓  ✓ | clean         — Reality lives, IP reachable (H1 holds).
//	   ✓     *  ✗  ✓ | reality_wins  — Reality punches through where bare TLS to
//	                 |                 the IP is already dead: the best outcome,
//	                 |                 direct proof the obfuscation earns its keep.
//	   ✗     ✓  *  ✓ | unknown       — the handshake worked and the relay
//	                 |                 forwarded us; what did not answer is our
//	                 |                 own upstream. Not the relay's failure,
//	                 |                 and naming it one would rekey a healthy
//	                 |                 fleet every time the gateway hiccups.
//	   ✗     ✗  ✓  ✓ | fingerprint   — IP reachable by bare TLS, and the Reality
//	                 |                 handshake itself is refused: the
//	                 |                 fingerprint was detected.
//	   ✗     ✗  ✗  ✓ | ip_banned     — IP unreachable on the port AND Reality down:
//	                 |                 the IP itself is blocked, Reality blameless.
//
// # Why k3 sits above k1 and not beside it
//
// Before П9.4 "reality" was one boolean over two different things — the
// handshake and the answer behind it — and a silent gateway therefore read as
// `fingerprint` on every relay at once. That is the worst possible reading:
// the repair for a burnt protocol is a re-key of every box, and the fleet
// would have been re-keyed over our own outage. So the handshake is measured
// apart, and it is checked before anything is blamed on the relay.
func classify(reality, k3, k1, k2 bool) string {
	if !k2 {
		return verdictNetworkDown
	}
	switch {
	case reality && k1:
		return verdictClean
	case reality && !k1:
		return verdictRealityWins
	case k3:
		return verdictUnknown
	case k1:
		return verdictFingerprint
	default: // !reality && !k3 && !k1
		return verdictIPBanned
	}
}

// classifyXhttp is the matrix for an XHTTP relay (П4в). The relay is a site
// on its own domain that answers any ClientHello with that domain's
// certificate, so the bare-TLS control splits in two:
//
//	tunnel — /healthz through the relay's XHTTP tunnel, certificate checked.
//	k3     — the tunnel carried a connection at all (П9.4): true with a silent
//	         upstream of our own, which is not the relay's failure.
//	named  — a bare TLS handshake to the relay with the domain as SNI.
//	bare   — the same handshake with no SNI: the address alone.
//	certOK — the named handshake's certificate verifies for the domain.
//	k2     — the network-is-up control, as for Reality.
//
//	tunnel k3 named bare k2 | verdict
//	------ -- ----- ---- -- | -------
//	   *    *    *    *   ✗ | network_down
//	   ✓    *    *    *   ✓ | clean
//	   ✗    ✓    *    *   ✓ | unknown         — forwarded us, our upstream was
//	                        |                   silent: not the relay
//	   ✗    ✗    ✓    *   ✓ | fingerprint     — the site answers by name, the
//	                     |                   tunnel is cut: XHTTP detected
//	                     |                   (unknown while the certificate
//	                     |                   does not verify: then it is ours)
//	   ✗    ✗    ✗    ✓   ✓ | domain_blocked  — the address answers, the name not
//	   ✗    ✗    ✗    ✗   ✓ | ip_banned
func classifyXhttp(tunnel, k3, named, bare, certOK, k2 bool) string {
	switch {
	case !k2:
		return verdictNetworkDown
	case tunnel:
		return verdictClean
	case k3:
		return verdictUnknown
	case named && !certOK:
		return verdictUnknown
	case named:
		return verdictFingerprint
	case bare:
		return verdictDomainBlocked
	default:
		return verdictIPBanned
	}
}

// classifyReprobe is the matrix for a burned domain's reprobe (roadmap 42
// §11). The name has no relay of its own any more, so it is tried as SNI
// against the address of a live XHTTP relay the hub lends — the same pair of
// handshakes that burned it in classifyXhttp, on an address known to answer.
// The certificate is not checked: the lent relay presents its own domain's,
// and the question is only whether the censor lets the name's ClientHello
// through.
//
//	named bare k2 | verdict
//	----- ---- -- | -------
//	  *     *   ✗ | network_down
//	  ✓     *   ✓ | clean           — the name passes
//	  ✗     ✓   ✓ | domain_blocked  — the address answers, the name not
//	  ✗     ✗   ✓ | unknown         — the lent address is down: nothing
//	              |                   learned about the name
func classifyReprobe(named, bare, k2 bool) string {
	switch {
	case !k2:
		return verdictNetworkDown
	case named:
		return verdictClean
	case bare:
		return verdictDomainBlocked
	default:
		return verdictUnknown
	}
}
