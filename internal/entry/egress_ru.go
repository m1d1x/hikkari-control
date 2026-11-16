package entry

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// ripeDelegatedURL is RIPE NCC's daily list of the blocks it delegated, with
// the country of each holder. Russian networks are RIPE's to delegate.
const ripeDelegatedURL = "https://ftp.ripe.net/pub/stats/ripencc/delegated-ripencc-extended-latest"

// ruNetsFloor: a list with fewer Russian blocks than this is a broken
// download, not the truth (RIPE delegates about 10 thousand IPv4 blocks
// and as many IPv6 to Russia).
const ruNetsFloor = 2000

// fetchRUNets downloads RIPE's delegated list and renders the Russian
// blocks as the nft elements of the egress's sets ru4 and ru6.
func fetchRUNets(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("RIPE ответил %d", resp.StatusCode)
	}
	v4, v6, err := parseDelegated(resp.Body, "RU")
	if err != nil {
		return "", err
	}
	if len(v4) < ruNetsFloor || len(v6) < ruNetsFloor {
		return "", fmt.Errorf("в списке RIPE только %d блоков IPv4 и %d IPv6 РФ", len(v4), len(v6))
	}
	return ruNetsElements(v4, v6), nil
}

// parseDelegated reads RIPE's delegated format
// (registry|cc|type|start|value|date|status[|...]) and returns the
// allocated and assigned blocks of country cc as prefixes.
func parseDelegated(r io.Reader, cc string) (v4, v6 []netip.Prefix, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 7 || f[1] != cc || (f[6] != "allocated" && f[6] != "assigned") {
			continue
		}
		switch f[2] {
		case "ipv4":
			start, err := netip.ParseAddr(f[3])
			if err != nil || !start.Is4() {
				return nil, nil, fmt.Errorf("строка RIPE %q", line)
			}
			n, err := strconv.ParseUint(f[4], 10, 32)
			if err != nil || n == 0 {
				return nil, nil, fmt.Errorf("строка RIPE %q", line)
			}
			v4 = append(v4, rangeToPrefixes(start, uint32(n))...)
		case "ipv6":
			start, err := netip.ParseAddr(f[3])
			bitsLen, err2 := strconv.Atoi(f[4])
			if err != nil || err2 != nil || !start.Is6() || bitsLen < 1 || bitsLen > 128 {
				return nil, nil, fmt.Errorf("строка RIPE %q", line)
			}
			v6 = append(v6, netip.PrefixFrom(start, bitsLen).Masked())
		}
	}
	return v4, v6, sc.Err()
}

// rangeToPrefixes covers n IPv4 addresses from start with the fewest
// prefixes (RIPE counts addresses, and a count is not always a power of two).
func rangeToPrefixes(start netip.Addr, n uint32) []netip.Prefix {
	b := start.As4()
	a := uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
	end := a + uint64(n)
	var out []netip.Prefix
	for a < end {
		// The largest block aligned at a that still fits.
		size := uint64(1) << 32
		if a != 0 {
			size = uint64(1) << bits.TrailingZeros64(a)
		}
		for size > end-a {
			size >>= 1
		}
		ip := netip.AddrFrom4([4]byte{byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a)})
		out = append(out, netip.PrefixFrom(ip, 32-bits.TrailingZeros64(size)))
		a += size
	}
	return out
}

// ruNetsElements renders the blocks as nft statements, a thousand to one.
func ruNetsElements(v4, v6 []netip.Prefix) string {
	var b strings.Builder
	for _, set := range []struct {
		name string
		nets []netip.Prefix
	}{{"ru4", v4}, {"ru6", v6}} {
		for i := 0; i < len(set.nets); i += 1000 {
			j := min(i+1000, len(set.nets))
			parts := make([]string, 0, j-i)
			for _, p := range set.nets[i:j] {
				parts = append(parts, p.String())
			}
			fmt.Fprintf(&b, "add element inet hikkari_egress %s { %s }\n", set.name, strings.Join(parts, ", "))
		}
	}
	return b.String()
}
