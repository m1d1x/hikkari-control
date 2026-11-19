package entry

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// A border point (hikkari-client plans/2026-11-19-border-forwarders.md, F1):
// a box abroad that only forwards the trunk's ports from the entries to the
// core, the box whose hk-exit and Hikkari relay hold the people's sessions.
// It keeps no key, no process and nothing of the people: a move of the trunk
// from one border point to another cuts nothing, the sessions live behind
// them. 19.11 every move between receivers cut all the people had, since
// each receiver's relay held their connections.
//
//	cmd/hub entry-box border-forward <box> <core-box>|off
//
// The box gets an nft table (DNAT of the core's trunk ports from the
// entries' addresses to the core, SNAT of what it forwards), forwarding on,
// and ufw's route rules for exactly those flows; a unit loads the table on
// boot. The box's own relays and ports are left as they are: only the
// entries' trunk is forwarded.

const (
	borderTable  = "hikkari_border"
	borderNFT    = "/etc/hikkari-border.nft"
	borderUnit   = "hikkari-border.service"
	borderSysctl = "/etc/sysctl.d/90-hikkari-border.conf"
)

// borderPlan is what a border point forwards: the entries' addresses, the
// core's address, its trunk's TCP ports (the relay's and the pool's) and UDP
// ports (the pipe's wires).
type borderPlan struct {
	Entries []netip.Addr
	Core    netip.Addr
	TCP     []int
	UDP     []int
}

// nft is the table for the plan, loaded whole (it deletes its old self).
func (b borderPlan) nft() string {
	set := func(xs []string) string { return "{ " + strings.Join(xs, ", ") + " }" }
	var entries, tcp, udp []string
	for _, e := range b.Entries {
		entries = append(entries, e.String())
	}
	for _, p := range b.TCP {
		tcp = append(tcp, strconv.Itoa(p))
	}
	for _, p := range b.UDP {
		udp = append(udp, strconv.Itoa(p))
	}
	var s strings.Builder
	fmt.Fprintf(&s, "table ip %s\ndelete table ip %s\ntable ip %s {\n", borderTable, borderTable, borderTable)
	s.WriteString("\tchain pre {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	if len(tcp) > 0 {
		fmt.Fprintf(&s, "\t\tip saddr %s tcp dport %s dnat to %s\n", set(entries), set(tcp), b.Core)
	}
	if len(udp) > 0 {
		fmt.Fprintf(&s, "\t\tip saddr %s udp dport %s dnat to %s\n", set(entries), set(udp), b.Core)
	}
	s.WriteString("\t}\n\tchain post {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	fmt.Fprintf(&s, "\t\tip daddr %s ct status dnat masquerade\n", b.Core)
	s.WriteString("\t}\n}\n")
	return s.String()
}

// ufw is ufw's route rules for the plan: ufw drops what is routed by
// default, and a drop in its chain is not undone by an accept in ours.
func (b borderPlan) ufw(verb string) []string {
	var out []string
	join := func(ps []int) string {
		s := make([]string, len(ps))
		for i, p := range ps {
			s[i] = strconv.Itoa(p)
		}
		return strings.Join(s, ",")
	}
	for _, e := range b.Entries {
		if len(b.TCP) > 0 {
			out = append(out, fmt.Sprintf("ufw %sroute allow proto tcp from %s to %s port %s comment hikkari-border", verb, e, b.Core, join(b.TCP)))
		}
		if len(b.UDP) > 0 {
			out = append(out, fmt.Sprintf("ufw %sroute allow proto udp from %s to %s port %s comment hikkari-border", verb, e, b.Core, join(b.UDP)))
		}
	}
	return out
}

// standScript puts the plan on the box, drops the entries' flows the box
// tracked to itself before (so the next datagram is forwarded), and says
// the table back.
func (b borderPlan) standScript() string {
	var s strings.Builder
	s.WriteString("set -eu\n")
	fmt.Fprintf(&s, "cat > %s <<'HKBORDER'\n%sHKBORDER\n", borderNFT, b.nft())
	fmt.Fprintf(&s, "echo net.ipv4.ip_forward=1 > %s\nsysctl -q -w net.ipv4.ip_forward=1\n", borderSysctl)
	fmt.Fprintf(&s, `cat > /etc/systemd/system/%s <<'HKBORDER'
[Unit]
Description=Hikkari border point: the trunk forwarded to its core
After=network-pre.target ufw.service
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f %s
ExecStop=/usr/sbin/nft delete table ip %s

[Install]
WantedBy=multi-user.target
HKBORDER
`, borderUnit, borderNFT, borderTable)
	fmt.Fprintf(&s, "systemctl daemon-reload\nsystemctl enable %s >/dev/null 2>&1\nnft -f %s\nsystemctl start %s\n", borderUnit, borderNFT, borderUnit)
	for _, c := range b.ufw("") {
		s.WriteString(c + " >/dev/null\n")
	}
	s.WriteString("command -v conntrack >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -y -q conntrack >/dev/null\n")
	for _, e := range b.Entries {
		for _, p := range b.UDP {
			fmt.Fprintf(&s, "conntrack -D -p udp -s %s --dport %d >/dev/null 2>&1 || true\n", e, p)
		}
	}
	fmt.Fprintf(&s, "nft list table ip %s\n", borderTable)
	return s.String()
}

// stopScript takes the plan off the box.
func (b borderPlan) stopScript() string {
	var s strings.Builder
	s.WriteString("set -u\n")
	fmt.Fprintf(&s, "systemctl disable --now %s >/dev/null 2>&1 || true\nnft delete table ip %s 2>/dev/null || true\n", borderUnit, borderTable)
	fmt.Fprintf(&s, "rm -f /etc/systemd/system/%s %s %s\nsystemctl daemon-reload\n", borderUnit, borderNFT, borderSysctl)
	for _, c := range b.ufw("delete ") {
		s.WriteString(c + " >/dev/null 2>&1 || true\n")
	}
	s.WriteString("echo border off\n")
	return s.String()
}

// borderPlanOf is what box forwards to core: the core's trunk ports, the
// entries whose trunk goes to the core.
func (s *Service) borderPlanOf(ctx context.Context, box, core string) (borderPlan, error) {
	if box == core {
		return borderPlan{}, fmt.Errorf("%w: ядро не пересылает само себе", ErrRejected)
	}
	acc, err := s.boxAccess(ctx, core)
	if err != nil {
		return borderPlan{}, err
	}
	coreIP, err := netip.ParseAddr(acc.address)
	if err != nil || !coreIP.Is4() {
		return borderPlan{}, fmt.Errorf("%w: у ядра %s нет IPv4-адреса", ErrRejected, core)
	}
	relays, err := s.relayIdentities(ctx, core)
	if err != nil {
		return borderPlan{}, err
	}
	plan := borderPlan{Core: coreIP}
	tcp, udp := map[int]bool{}, map[int]bool{}
	for _, rel := range relays {
		h := rel.id.Hikkari
		if h == nil || h.TrunkPipeKey == "" {
			continue
		}
		tcp[rel.id.Port] = true
		for _, p := range h.TrunkPorts {
			tcp[p] = true
		}
		for _, p := range h.TrunkPipeWires {
			udp[p] = true
		}
	}
	if len(tcp) == 0 {
		return borderPlan{}, fmt.Errorf("%w: у %s нет hk-exit под релеем, сначала entry-box trunk-pipe-exit", ErrRejected, core)
	}
	for p := range tcp {
		plan.TCP = append(plan.TCP, p)
	}
	for p := range udp {
		plan.UDP = append(plan.UDP, p)
	}
	sort.Ints(plan.TCP)
	sort.Ints(plan.UDP)
	rows, err := s.pool.Query(ctx, `SELECT entry_box FROM trunk_entries WHERE exit_box = $1 ORDER BY entry_box`, core)
	if err != nil {
		return borderPlan{}, err
	}
	var boxes []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return borderPlan{}, err
		}
		boxes = append(boxes, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return borderPlan{}, err
	}
	for _, b := range boxes {
		a, err := s.boxAccess(ctx, b)
		if err != nil {
			return borderPlan{}, err
		}
		host := a.address
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if ip, err := netip.ParseAddr(host); err == nil && ip.Is4() {
			plan.Entries = append(plan.Entries, ip)
		}
	}
	if len(plan.Entries) == 0 {
		return borderPlan{}, fmt.Errorf("%w: магистраль ни одного входа не идёт в %s", ErrRejected, core)
	}
	return plan, nil
}

// StandBorder makes box a border point of core, or takes it off (core
// "off"; the table is taken whole, whatever core it forwarded to).
func (p *Provisioner) StandBorder(ctx context.Context, box, core string) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	if core == "off" {
		out, err := r.Run(ctx, "bash -s", strings.NewReader(borderPlan{}.stopScript()+stopBorderRoutes))
		if err != nil {
			return fmt.Errorf("%s: %w: %s", box, err, out)
		}
		applog.Info(ctx, "entry", "operator border point off", applog.F("box", box))
		fmt.Print(out)
		return nil
	}
	plan, err := p.svc.borderPlanOf(ctx, box, core)
	if err != nil {
		return err
	}
	out, err := r.Run(ctx, "bash -s", strings.NewReader(plan.standScript()))
	if err != nil {
		return fmt.Errorf("%s: %w: %s", box, err, out)
	}
	applog.Info(ctx, "entry", "operator border point", applog.F("box", box), applog.F("core", core),
		applog.F("tcp", fmt.Sprint(plan.TCP)), applog.F("udp", fmt.Sprint(plan.UDP)), applog.F("entries", len(plan.Entries)))
	fmt.Print(out)
	return nil
}

// stopBorderRoutes deletes every ufw route rule the border point put, by
// its comment: off knows no plan.
const stopBorderRoutes = `for n in $(ufw status numbered | grep hikkari-border | sed -E 's/^\[ *([0-9]+)\].*/\1/' | sort -rn); do yes | ufw delete "$n" >/dev/null; done
`
