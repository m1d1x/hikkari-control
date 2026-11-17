package entry

import (
	"context"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A probe call through the receiver's tunnel while the egress changes (H3:
// "смена выхода под живым звонком записана с паузой в мс"). It runs on the
// receiver as the routed user, so the kernel takes it into the tunnel the
// way it takes a relay's flows: a STUN binding request every 20 ms to
// Cloudflare's anycast, which answers near whichever egress the request left
// from, and the longest silence between two answers is the pause a call
// through the receiver hears. The probe needs python3 on the receiver; without
// it the move goes on unmeasured.

const (
	egressCallUnit = "hk-egress-call"
	egressCallOut  = "/tmp/hk-egress-call.out"
	// egressCallSecs covers the swap with a few seconds of call on both
	// sides of it.
	egressCallSecs = 14
	egressCallHost = "stun.cloudflare.com"
)

var (
	// egressCallLead is the call before the swap: the steady pause to read
	// the swap's against.
	egressCallLead = 3 * time.Second
	// egressCallLookup resolves the STUN server on the hub: the receiver's
	// own resolver would ask from the receiver's address.
	egressCallLookup = net.DefaultResolver.LookupNetIP
)

// egressCallPy sends and counts; argv: seconds, the STUN address, the output.
const egressCallPy = `import os, socket, struct, sys, time
dur, host, out = float(sys.argv[1]), sys.argv[2], sys.argv[3]
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.connect((host, ` + "3478" + `))
s.setblocking(False)
sent = recv = 0
last = None
gap = 0.0
t0 = time.monotonic()
nxt = t0
while time.monotonic() - t0 < dur:
    now = time.monotonic()
    if now >= nxt:
        try:
            s.send(struct.pack(">HHI", 1, 0, 0x2112A442) + os.urandom(12))
            sent += 1
        except OSError:
            pass
        nxt += 0.02
    try:
        d = s.recv(2048)
        if len(d) >= 20 and d[:2] == b"\x01\x01":
            recv += 1
            t = time.monotonic()
            if last is not None:
                gap = max(gap, t - last)
            last = t
    except (BlockingIOError, ConnectionRefusedError):
        time.sleep(0.002)
with open(out, "w") as f:
    f.write("sent=%d recv=%d gap_max_ms=%d\n" % (sent, recv, gap * 1000))
`

var egressCallRe = regexp.MustCompile(`sent=(\d+) recv=(\d+) gap_max_ms=(\d+)`)

// egressCall is a probe call running on the receiver.
type egressCall struct{}

// startEgressCall starts the call and lets it run its lead before the swap;
// nil when it cannot run (the move is not stopped for it).
func startEgressCall(ctx context.Context, e *egressEnds) *egressCall {
	addrs, err := egressCallLookup(ctx, "ip4", egressCallHost)
	if err != nil || len(addrs) == 0 {
		e.rxLog("звонок через выход не запущен: %s не резолвится: %v", egressCallHost, err)
		return nil
	}
	cmd := "command -v python3 >/dev/null || { echo 'нет python3' >&2; exit 3; }; rm -f " + egressCallOut + "; " +
		"systemctl reset-failed " + egressCallUnit + " >/dev/null 2>&1 || true; " +
		"systemd-run --quiet --collect --uid=" + EgressUser + " --unit=" + egressCallUnit +
		" -p RuntimeMaxSec=" + strconv.Itoa(egressCallSecs+10) +
		" python3 -c \"$(cat)\" " + strconv.Itoa(egressCallSecs) + " " + addrs[0].String() + " " + egressCallOut
	if _, err := e.rx.Run(ctx, cmd, strings.NewReader(egressCallPy)); err != nil {
		e.rxLog("звонок через выход не запущен: %v", err)
		return nil
	}
	c := &egressCall{}
	select {
	case <-ctx.Done():
	case <-time.After(egressCallLead):
	}
	return c
}

// finish waits for the call to end and writes what it heard.
func (c *egressCall) finish(ctx context.Context, e *egressEnds) {
	wait := egressCallSecs + 5
	out, err := e.rx.Run(ctx, "for _ in $(seq 1 "+strconv.Itoa(wait*2)+"); do systemctl is-active --quiet "+egressCallUnit+" || break; sleep 0.5; done; cat "+egressCallOut, nil)
	if err != nil {
		e.rxLog("звонок через выход не дописался: %v", err)
		return
	}
	m := egressCallRe.FindStringSubmatch(out)
	if m == nil {
		e.rxLog("звонок через выход: %s", remoteWord(strings.TrimSpace(out)))
		return
	}
	e.rxLog("звонок через выход при смене: пауза %s мс, ответов %s из %s", m[3], m[2], m[1])
}
