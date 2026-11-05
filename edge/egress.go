package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"strconv"
	"strings"
)

// A Hikkari edge is a VPN exit: whatever a client's tunnel asks for leaves the
// box through one freedom outbound, "direct". Nekko's edge was a pipe to one
// gateway and dropped everything else; here the allowlist is the internet and
// the denylist is what a VPN exit must never reach on a user's behalf:
//
//   - the box itself and the hoster's private network (loopback, RFC 1918,
//     link-local with the cloud metadata service at 169.254.169.254, CGNAT,
//     multicast, reserved), so a client cannot talk to services bound on the
//     box or to the provider's internal endpoints;
//   - outbound SMTP (port 25): the first thing an abused exit sends, and the
//     reason a hoster suspends a machine without asking.
//
// The box has IPv6, so both families are refused alike. The IPv6 forms that
// carry an IPv4 address (NAT64, 6to4, Teredo) are refused whole: through them a
// client would reach what the IPv4 list refuses. Xray reads a mapped address
// (::ffff:a.b.c.d) as the IPv4 itself, so the IPv4 rules already cover it.
//
// The rules are freedom's finalRules, checked against the address actually
// dialled — after a domain is resolved — so a name that resolves into a
// private range is refused as well. The one loopback destination allowed is
// the edge's own UDP echo, which the admission probe times through the tunnel.

// directTag is the outbound every tunnel's traffic leaves through.
const directTag = "direct"

// dropTag is the blackhole outbound for refused destinations.
const dropTag = "drop"

// privateNets are the box itself and the hoster's internal networks.
var privateNets = []string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"::1/128",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/32",
	"2001:db8::/32",
	"2002::/16",
	"fc00::/7",
	"fe80::/10",
	"fec0::/10",
	"ff00::/8",
}

//go:embed ru_cidrs.txt.gz
var ruCIDRsGz []byte

// blockedRUNets holds the full RIPE/ipdeny Russian CIDR table, IPv4 (8,600+
// collapsed blocks) then IPv6 (2,100+), embedded directly in the binary so
// nekko-edge needs no external geoip.dat on disk. The IPv6 part is every RU
// ipv6 record of RIPE's delegated stats and ipdeny's ru-aggregated zone, nested
// blocks dropped. The other four RIRs list no RU IPv6.
var blockedRUNets = loadBlockedRUNets()

func loadBlockedRUNets() []string {
	gz, err := gzip.NewReader(bytes.NewReader(ruCIDRsGz))
	if err != nil {
		panic("edge: corrupt embedded ru_cidrs.txt.gz: " + err.Error())
	}
	defer gz.Close()
	raw, err := io.ReadAll(gz)
	if err != nil {
		panic("edge: reading embedded ru_cidrs.txt.gz: " + err.Error())
	}
	return strings.Fields(string(raw))
}

// tunnelSniffing inspects TLS SNI, HTTP Host and QUIC SNI inside the tunnel
// stream (routeOnly: true) so a client connecting by raw IP (e.g. via browser
// DoH or to a .ru site behind a foreign CDN) still triggers blockedDomains.
func tunnelSniffing() map[string]any {
	return map[string]any{
		"enabled":      true,
		"destOverride": []string{"http", "tls", "quic"},
		"routeOnly":    true,
	}
}

// blockedNets are never dialled through the tunnel (both private ranges and
// Russian IP allocations).
var blockedNets = append(append([]string{}, privateNets...), blockedRUNets...)

// blockedDomains are Russian domain zones and key Russian ecosystems that must
// never be reached from a VPN exit node.
var blockedDomains = []string{
	// Russian & Soviet TLDs
	"domain:ru",
	"domain:xn--p1ai",     // .рф
	"domain:su",           // .su
	"domain:xn--p1acf",    // .рус
	"domain:xn--80adxhks", // .москва
	"domain:moscow",
	"domain:tatar",
	"domain:xn--d1acj3b",  // .дети
	"domain:xn--80asehdb", // .онлайн
	"domain:xn--80aswg",   // .сайт
	// Yandex ecosystem
	"domain:yandex.ru",
	"domain:ya.ru",
	"domain:yandex.com",
	"domain:yandex.net",
	"domain:yandex.by",
	"domain:yandex.kz",
	"domain:yandex.uz",
	"domain:yandex.st",
	"domain:yandex.sx",
	"domain:yandexcloud.net",
	"domain:yastatic.net",
	"domain:kinopoisk.ru",
	"domain:dzen.ru",
	// VK / Mail.ru ecosystem
	"domain:vk.com",
	"domain:vk.ru",
	"domain:vk.me",
	"domain:vkuser.net",
	"domain:vkuseraudio.net",
	"domain:vkuservideo.net",
	"domain:vkvideo.ru",
	"domain:userapi.com",
	"domain:mycdn.me",
	"domain:ok.ru",
	"domain:odnoklassniki.ru",
	"domain:mail.ru",
	"domain:imgsmail.ru",
	// Russian banks & payment systems
	"domain:sberbank.ru",
	"domain:sberbank.com",
	"domain:sber.ru",
	"domain:sberdevices.ru",
	"domain:tbank.ru",
	"domain:tinkoff.ru",
	"domain:tinkoff.com",
	"domain:tcsbank.ru",
	"domain:vtb.ru",
	"domain:vtb.com",
	"domain:alfabank.ru",
	"domain:alfabank.com",
	"domain:gazprombank.ru",
	"domain:raiffeisen.ru",
	"domain:sovcombank.ru",
	"domain:rshb.ru",
	"domain:psbank.ru",
	"domain:mkb.ru",
	"domain:uralsib.ru",
	"domain:tochka.com",
	"domain:nspk.ru",
	"domain:mirpay.ru",
	"domain:cbr.ru",
	"domain:moex.com",
	// State & government services
	"domain:gosuslugi.ru",
	"domain:gov.ru",
	"domain:mos.ru",
	"domain:nalog.ru",
	"domain:kremlin.ru",
	"domain:rzd.ru",
	"domain:pochta.ru",
	// Marketplaces, retail, classifieds & media
	"domain:ozon.ru",
	"domain:ozone.ru",
	"domain:ozonusercontent.com",
	"domain:wildberries.ru",
	"domain:wb.ru",
	"domain:wbbasket.ru",
	"domain:avito.ru",
	"domain:avito.st",
	"domain:rutube.ru",
	"domain:ivi.ru",
	"domain:okko.tv",
	"domain:wink.ru",
	"domain:premier.one",
	"domain:smotrim.ru",
	"domain:kion.ru",
	"domain:2gis.ru",
	"domain:2gis.com",
	"domain:mts.ru",
	"domain:megafon.ru",
	"domain:beeline.ru",
	"domain:t2.ru",
	"domain:rostelecom.ru",
	"domain:kaspersky.ru",
	"domain:kaspersky.com",
	"domain:drweb.com",
}

// blockedPorts are refused on every address.
const blockedPorts = "25"

// egressFinalRules: the echo, then the denylist, then everything else allowed.
// With finalRules set freedom follows them alone (its built-in refusal of
// private addresses no longer applies), so the list states every case.
func egressFinalRules(echo int) []map[string]any {
	var rules []map[string]any
	if echo > 0 {
		rules = append(rules, map[string]any{
			"action": "allow", "network": "udp", "ip": []string{"127.0.0.1/32"}, "port": strconv.Itoa(echo),
		})
	}
	return append(rules,
		map[string]any{"action": "block", "port": blockedPorts},
		map[string]any{"action": "block", "ip": blockedNets},
		map[string]any{"action": "allow", "ip": []string{"0.0.0.0/0", "::/0"}},
	)
}

// directOutbound is the exit itself.
func directOutbound(echo int) map[string]any {
	return map[string]any{
		"tag":      directTag,
		"protocol": "freedom",
		"settings": map[string]any{
			// Resolve on the box, so finalRules see the real address. IPv4
			// first, IPv6 only for a name with no A record: UseIP would pick
			// at random, moving IPv4 sites onto the box's IPv6 whenever they
			// also have an AAAA record.
			"domainStrategy": "UseIPv4v6",
			"finalRules":     egressFinalRules(echo),
		},
	}
}

// egressRule sends every tunnel inbound in tags to the exit.
func egressRule(tags []string) map[string]any {
	return map[string]any{
		"type":        "field",
		"inboundTag":  tags,
		"outboundTag": directTag,
	}
}

// egressRules blocks Russian domains and IP ranges on the router before
// sending the remaining tunnel traffic to the exit.
func egressRules(tags []string) []map[string]any {
	return []map[string]any{
		{
			"type":        "field",
			"inboundTag":  tags,
			"domain":      blockedDomains,
			"outboundTag": dropTag,
		},
		{
			"type":        "field",
			"inboundTag":  tags,
			"ip":          blockedRUNets,
			"outboundTag": dropTag,
		},
		egressRule(tags),
	}
}
