package entry

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// A border point (hikkari-client plans/2026-11-19-border-forwarders.md, F1,
// G2): a box abroad that only forwards the trunk's ports from the entries to
// a core, the box whose hk-exit and Hikkari relay hold the sessions. It keeps
// no key, no process and nothing of the people: a move of the trunk from one
// border point to another cuts nothing, the sessions live behind them. 19.11
// every move between receivers cut all the people had, since each
// receiver's relay held their connections.
//
//	cmd/hub entry-box border-forward <box> <core-box>       # forward to that core
//	cmd/hub entry-box border-forward <box> <core-box> off   # that core's forwarding off
//	cmd/hub entry-box border-forward <box> off              # every core's off
//
// The box gets an nft table of its own per core (DNAT of the core's trunk
// ports from the entries' addresses to the core, SNAT of what it forwards),
// forwarding on, and ufw's route rules for exactly those flows; a unit per
// core loads its table on boot. One box may forward to several cores at once
// (G2: ams3 forwards the people's trunk to unl1 and the stand's to aeznl1),
// so the cores' trunk ports must differ. The box's own relays and ports are
// left as they are: only the entries' trunk is forwarded.
//
// The trunk's ports of a core are its Hikkari relay's port (TCP: the pool's
// members dial it, the relay hands the trunk's streams to hk-exit) and
// hk-exit's wires (UDP). TrunkPorts are loopback ports behind the exit, never
// dialled from outside, and are not forwarded.
//
// The core opens those ports to the point's address (ufw, comment
// hikkari-border-in-<point>): a core on an egress box (entry-box core)
// opens them to nobody else, so from Russia it answers nobody.
//
// A border point is abroad, so it cuts its own new connections to Russian
// networks at the kernel, as the egress does (the owner's word of 20.11:
// «запрет на РФ из заграницы должен быть строго везде и всегда»): a table
// hikkari_noru with RIPE's Russian blocks and a unit that loads it on boot.
// It stays when the forwarding goes off: the box is abroad all the same.
//
// The entries are the boxes whose trunk goes to the core: the people's
// (trunk_entries.exit_box) and the stand's over hk-pipe
// (trunk_entries.pipe_core, entry-box trunk-pipe ... core=<box>).

const (
	// borderLegacy is F1's single table, before one per core: a forward to
	// a core takes it off once the core's own table is in, when it forwarded
	// to the same core.
	borderLegacy = "hikkari_border"
	borderSysctl = "/etc/sysctl.d/90-hikkari-border.conf"
)

// borderCoreName is a core box's name as it goes into the nft table, the
// unit and the ufw comment.
var borderCoreName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// borderPlan is what a border point forwards to one core: the entries'
// addresses, the core's name and address, its trunk's TCP ports (the
// relay's) and UDP ports (the pipe's wires).
type borderPlan struct {
	Entries []netip.Addr
	CoreBox string
	Core    netip.Addr
	TCP     []int
	UDP     []int
}

// table, file, unit and comment are the plan's names on the box.
func (b borderPlan) table() string {
	return borderLegacy + "_" + strings.ReplaceAll(b.CoreBox, "-", "_")
}
func (b borderPlan) file() string    { return "/etc/hikkari-border-" + b.CoreBox + ".nft" }
func (b borderPlan) unit() string    { return "hikkari-border-" + b.CoreBox + ".service" }
func (b borderPlan) comment() string { return "hikkari-border-" + b.CoreBox }

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
	t := b.table()
	var s strings.Builder
	fmt.Fprintf(&s, "table ip %s\ndelete table ip %s\ntable ip %s {\n", t, t, t)
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

// joinPorts is ports as ufw takes a list of them.
func joinPorts(ps []int) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

// ufw is ufw's route rules for the plan: ufw drops what is routed by
// default, and a drop in its chain is not undone by an accept in ours.
func (b borderPlan) ufw(verb string) []string {
	var out []string
	for _, e := range b.Entries {
		if len(b.TCP) > 0 {
			out = append(out, fmt.Sprintf("ufw %sroute allow proto tcp from %s to %s port %s comment %s", verb, e, b.Core, joinPorts(b.TCP), b.comment()))
		}
		if len(b.UDP) > 0 {
			out = append(out, fmt.Sprintf("ufw %sroute allow proto udp from %s to %s port %s comment %s", verb, e, b.Core, joinPorts(b.UDP), b.comment()))
		}
	}
	return out
}

// ufwDeleteByComment deletes every ufw rule whose comment is exactly c.
func ufwDeleteByComment(c string) string {
	return fmt.Sprintf("for n in $(ufw status numbered | grep -E '# %s$' | sed -E 's/^\\[ *([0-9]+)\\].*/\\1/' | sort -rn); do yes | ufw delete \"$n\" >/dev/null; done\n", regexp.QuoteMeta(c))
}

// standScript puts the plan on the box, drops the entries' flows the box
// tracked to itself before (so the next datagram is forwarded), takes F1's
// single table off when it forwarded to this core, and says the table back.
func (b borderPlan) standScript() string {
	var s strings.Builder
	s.WriteString("set -eu\n")
	fmt.Fprintf(&s, "cat > %s <<'HKBORDER'\n%sHKBORDER\n", b.file(), b.nft())
	fmt.Fprintf(&s, "echo net.ipv4.ip_forward=1 > %s\nsysctl -q -w net.ipv4.ip_forward=1\n", borderSysctl)
	fmt.Fprintf(&s, `cat > /etc/systemd/system/%s <<'HKBORDER'
[Unit]
Description=Hikkari border point: the trunk forwarded to its core %s
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
`, b.unit(), b.CoreBox, b.file(), b.table())
	fmt.Fprintf(&s, "systemctl daemon-reload\nsystemctl enable %s >/dev/null 2>&1\nnft -f %s\nsystemctl start %s\n", b.unit(), b.file(), b.unit())
	for _, c := range b.ufw("") {
		s.WriteString(c + " >/dev/null\n")
	}
	// F1's single table to this core gives way to the core's own, now in:
	// the same DNAT twice for a moment, never none.
	fmt.Fprintf(&s, `if [ -f /etc/hikkari-border.nft ] && grep -q 'dnat to %s$' /etc/hikkari-border.nft; then
	systemctl disable --now hikkari-border.service >/dev/null 2>&1 || true
	nft delete table ip %s 2>/dev/null || true
	rm -f /etc/systemd/system/hikkari-border.service /etc/hikkari-border.nft
	systemctl daemon-reload
	%s	echo "F1 table to %s taken off"
fi
`, b.Core, borderLegacy, ufwDeleteByComment("hikkari-border"), b.Core)
	s.WriteString("command -v conntrack >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -y -q conntrack >/dev/null\n")
	for _, e := range b.Entries {
		for _, p := range b.UDP {
			fmt.Fprintf(&s, "conntrack -D -p udp -s %s --dport %d >/dev/null 2>&1 || true\n", e, p)
		}
	}
	fmt.Fprintf(&s, "nft list table ip %s\n", b.table())
	return s.String()
}

// stopScript takes the forwarding to this core off the box; the other
// cores' stay.
func (b borderPlan) stopScript() string {
	var s strings.Builder
	s.WriteString("set -u\n")
	fmt.Fprintf(&s, "systemctl disable --now %s >/dev/null 2>&1 || true\nnft delete table ip %s 2>/dev/null || true\n", b.unit(), b.table())
	fmt.Fprintf(&s, "rm -f /etc/systemd/system/%s %s\nsystemctl daemon-reload\n", b.unit(), b.file())
	s.WriteString(ufwDeleteByComment(b.comment()))
	fmt.Fprintf(&s, "ls /etc/hikkari-border*.nft >/dev/null 2>&1 || rm -f %s\n", borderSysctl)
	fmt.Fprintf(&s, "echo border to %s off\n", b.CoreBox)
	return s.String()
}

// stopAllBorders takes every core's forwarding off the box, F1's single
// table too.
const stopAllBorders = `set -u
for u in $(systemctl list-unit-files 'hikkari-border*.service' --no-legend 2>/dev/null | cut -d' ' -f1); do systemctl disable --now "$u" >/dev/null 2>&1 || true; rm -f "/etc/systemd/system/$u"; done
for t in $(nft list tables ip 2>/dev/null | sed -n 's/^table ip \(hikkari_border[a-z0-9_]*\)$/\1/p'); do nft delete table ip "$t"; done
rm -f /etc/hikkari-border*.nft ` + borderSysctl + `
systemctl daemon-reload
for n in $(ufw status numbered | grep -E '# hikkari-border(-[a-z0-9-]+)?$' | grep -v '# hikkari-border-in-' | sed -E 's/^\[ *([0-9]+)\].*/\1/' | sort -rn); do yes | ufw delete "$n" >/dev/null; done
echo border off
`

// noRUTable is a foreign box's own cut of new connections to Russia.
const noRUTable = "hikkari_noru"

// noRUScript loads the cut on the box with RIPE's Russian blocks (ruNets,
// fetchRUNets's elements of the egress's sets), in one transaction, and says
// its counter.
func noRUScript(ruNets string) string {
	elems := strings.ReplaceAll(ruNets, "inet hikkari_egress ", "inet "+noRUTable+" ")
	return `set -eu
cat > /etc/hikkari-noru.nft <<'HKNORU'
table inet ` + noRUTable + `
delete table inet ` + noRUTable + `
table inet ` + noRUTable + ` {
	set ru4 { type ipv4_addr; flags interval; auto-merge; }
	set ru6 { type ipv6_addr; flags interval; auto-merge; }
	counter ru_cut {}
	chain box_out {
		type filter hook output priority filter - 5; policy accept;
		# Into the egress's tunnel: the egress cuts Russia there and counts
		# it (H6 checks that count), the guard drops it when the tunnel is down.
		oifname "hk-egress" return
		ct state new ip daddr @ru4 counter name "ru_cut" reject with icmpx type admin-prohibited
		ct state new ip6 daddr @ru6 counter name "ru_cut" reject with icmpx type admin-prohibited
	}
}
` + elems + `HKNORU
cat > /etc/systemd/system/hikkari-noru.service <<'HKNORU'
[Unit]
Description=Hikkari: a box abroad opens nothing to Russia
After=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/hikkari-noru.nft

[Install]
WantedBy=multi-user.target
HKNORU
systemctl daemon-reload
systemctl enable hikkari-noru.service >/dev/null 2>&1
nft -f /etc/hikkari-noru.nft
nft list counter inet ` + noRUTable + ` ru_cut | grep -o 'packets [0-9]*'
`
}

// coreOpenScript opens the core's trunk ports to the point's address on the
// core, and coreCloseScript takes them back.
func (b borderPlan) coreOpenScript(point netip.Addr, pointBox string) string {
	var s strings.Builder
	s.WriteString("set -eu\n")
	for _, p := range b.TCP {
		fmt.Fprintf(&s, "ufw allow proto tcp from %s to any port %d comment hikkari-border-in-%s >/dev/null\n", point, p, pointBox)
	}
	for _, p := range b.UDP {
		fmt.Fprintf(&s, "ufw allow proto udp from %s to any port %d comment hikkari-border-in-%s >/dev/null\n", point, p, pointBox)
	}
	fmt.Fprintf(&s, "ufw status | grep -c 'hikkari-border-in-%s$' || true\n", pointBox)
	return s.String()
}

func coreCloseScript(pointBox string) string {
	return "set -u\n" + ufwDeleteByComment("hikkari-border-in-"+pointBox) + "echo core closed to " + pointBox + "\n"
}

// borderPlanOf is what box forwards to core: the core's trunk ports, the
// entries whose trunk goes to the core.
func (s *Service) borderPlanOf(ctx context.Context, box, core string) (borderPlan, error) {
	if box == core {
		return borderPlan{}, fmt.Errorf("%w: ядро не пересылает само себе", ErrRejected)
	}
	if !borderCoreName.MatchString(core) {
		return borderPlan{}, fmt.Errorf("%w: имя ядра %q", ErrRejected, core)
	}
	coreIP, err := s.boxIPv4(ctx, core)
	if err != nil {
		return borderPlan{}, err
	}
	relays, err := s.relayIdentities(ctx, core)
	if err != nil {
		return borderPlan{}, err
	}
	plan := borderPlan{CoreBox: core, Core: coreIP}
	tcp, udp := map[int]bool{}, map[int]bool{}
	for _, rel := range relays {
		h := rel.id.Hikkari
		if h == nil || h.TrunkPipeKey == "" {
			continue
		}
		tcp[rel.id.Port] = true
		for _, p := range h.TrunkPipeWires {
			udp[p] = true
		}
	}
	if len(tcp) == 0 {
		return borderPlan{}, fmt.Errorf("%w: у %s нет hk-exit под релеем, сначала entry-box trunk-pipe-exit или entry-box core", ErrRejected, core)
	}
	for p := range tcp {
		plan.TCP = append(plan.TCP, p)
	}
	for p := range udp {
		plan.UDP = append(plan.UDP, p)
	}
	sort.Ints(plan.TCP)
	sort.Ints(plan.UDP)
	rows, err := s.pool.Query(ctx, `SELECT entry_box FROM trunk_entries WHERE exit_box = $1 OR pipe_core = $1 ORDER BY entry_box`, core)
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
		if ip, err := s.boxIPv4(ctx, b); err == nil {
			plan.Entries = append(plan.Entries, ip)
		}
	}
	if len(plan.Entries) == 0 {
		return borderPlan{}, fmt.Errorf("%w: магистраль ни одного входа не идёт в %s", ErrRejected, core)
	}
	return plan, nil
}

// StandBorder makes box a border point of core; off takes that core's
// forwarding off the box (core "off" alone: every core's, F1's table too)
// and closes the core to the box.
func (p *Provisioner) StandBorder(ctx context.Context, box, core string, off bool) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	if core == "off" {
		out, err := r.Run(ctx, "bash -s", strings.NewReader(stopAllBorders))
		if err != nil {
			return fmt.Errorf("%s: %w: %s", box, err, out)
		}
		applog.Info(ctx, "entry", "operator border point off", applog.F("box", box))
		fmt.Print(out)
		return nil
	}
	if off {
		if !borderCoreName.MatchString(core) {
			return fmt.Errorf("%w: имя ядра %q", ErrRejected, core)
		}
		out, err := r.Run(ctx, "bash -s", strings.NewReader(borderPlan{CoreBox: core}.stopScript()))
		if err != nil {
			return fmt.Errorf("%s: %w: %s", box, err, out)
		}
		fmt.Print(out)
		if out, err := p.onCore(ctx, core, coreCloseScript(box)); err != nil {
			return err
		} else {
			fmt.Print(out)
		}
		applog.Info(ctx, "entry", "operator border point off", applog.F("box", box), applog.F("core", core))
		return nil
	}
	plan, err := p.svc.borderPlanOf(ctx, box, core)
	if err != nil {
		return err
	}
	point, err := p.svc.boxIPv4(ctx, box)
	if err != nil {
		return err
	}
	ruNets, err := fetchRUNets(ctx, ripeDelegatedURL)
	if err != nil {
		return fmt.Errorf("сети РФ для запрета на точке: %w", err)
	}
	out, err := r.Run(ctx, "bash -s", strings.NewReader(noRUScript(ruNets)))
	if err != nil {
		return fmt.Errorf("%s: запрет РФ: %w: %s", box, err, out)
	}
	fmt.Printf("%s: no Russia from the box (%s)\n", box, strings.TrimSpace(out))
	// The core first: a DNAT to a core that drops the point's flows is a
	// trunk cut, a core open to a point that forwards nothing yet is not.
	out, err = p.onCore(ctx, core, plan.coreOpenScript(point, box))
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s rules for %s\n", core, strings.TrimSpace(out), box)
	out, err = r.Run(ctx, "bash -s", strings.NewReader(plan.standScript()))
	if err != nil {
		return fmt.Errorf("%s: %w: %s", box, err, out)
	}
	applog.Info(ctx, "entry", "operator border point", applog.F("box", box), applog.F("core", core),
		applog.F("tcp", fmt.Sprint(plan.TCP)), applog.F("udp", fmt.Sprint(plan.UDP)), applog.F("entries", len(plan.Entries)))
	fmt.Print(out)
	return nil
}

// onCore runs script on the core box.
func (p *Provisioner) onCore(ctx context.Context, core, script string) (string, error) {
	r, _, err := p.provokeRemote(ctx, core)
	if err != nil {
		return "", err
	}
	defer r.Close()
	out, err := r.Run(ctx, "bash -s", strings.NewReader(script))
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", core, err, out)
	}
	return out, nil
}
