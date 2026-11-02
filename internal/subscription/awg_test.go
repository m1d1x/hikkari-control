package subscription

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

func awgRecord() entry.RelayRecord {
	return entry.RelayRecord{
		Name: "eun20-1", Address: "203.0.113.9", Port: 31000, Proto: entry.ProtoAmneziaWG,
		AwgParams: &entry.AwgParams{
			ServerPublicKey: "SERVERPUB=", ClientPrivateKey: "SHARED=", ClientAddress: entry.AwgProbeAddress,
			Jc: 4, Jmin: 40, Jmax: 70, S1: 33, S2: 57, H1: "11", H2: "12", H3: "13", H4: "14",
		},
	}
}

// The link is one line, and inside is what Amnezia's format says: qCompress with
// its size header, a server description, the .conf text in last_config.
func TestAwgLinkIsAmneziasFormatOnOneLine(t *testing.T) {
	link, err := AwgLink(awgRecord())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "vpn://") || strings.ContainsAny(link, "\n ") {
		t.Fatalf("link = %q", link)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "vpn://"))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw[4:]))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if size := binary.BigEndian.Uint32(raw[:4]); int(size) != len(doc) {
		t.Errorf("size header %d, payload %d", size, len(doc))
	}
	var got struct {
		HostName   string `json:"hostName"`
		Containers []struct {
			Container string `json:"container"`
			Awg       struct {
				LastConfig string `json:"last_config"`
			} `json:"awg"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	conf := got.Containers[0].Awg.LastConfig
	for _, want := range []string{"PrivateKey = SHARED=", "Address = 10.66.0.1/32", "Jc = 4", "H4 = 14", "PublicKey = SERVERPUB=", "Endpoint = 203.0.113.9:31000"} {
		if !strings.Contains(conf, want) {
			t.Errorf(".conf lacks %q:\n%s", want, conf)
		}
	}
}

// A link carries the subscriber's own key, never the vantage's shared one, and
// the record the hub holds is not changed by it.
func TestAwgWithKeyPutsTheAccountsOwnKeyIn(t *testing.T) {
	rec := awgRecord()
	got := withKey(rec, accessKey{awgPrivate: "MINE=", awgAddress: "10.66.0.7"})
	if got.AwgParams.ClientPrivateKey != "MINE=" || got.AwgParams.ClientAddress != "10.66.0.7" {
		t.Fatalf("%+v", got.AwgParams)
	}
	if rec.AwgParams.ClientPrivateKey != "SHARED=" {
		t.Fatal("the record itself was changed")
	}
}

func TestAwgAddressesStayInTheNetworkAndApart(t *testing.T) {
	seen := map[string]bool{}
	for n := int64(1); n <= 254*255; n++ {
		a, err := awgAddress(n)
		if err != nil {
			t.Fatal(err)
		}
		if seen[a] {
			t.Fatalf("%s given twice", a)
		}
		seen[a] = true
		if strings.HasSuffix(a, ".0") || strings.HasSuffix(a, ".255") {
			t.Fatalf("n=%d: %s", n, a)
		}
	}
	if first, _ := awgAddress(1); first != entry.AwgProbeAddress {
		t.Errorf("address 1 = %s, want the vantage's", first)
	}
	if _, err := awgAddress(254*255 + 1); err == nil {
		t.Error("an address past the network")
	}
}

// An account gets one key pair and one address however many times its link is
// asked for. A relay's list has it only from then on, in the edge's format, and
// what the edge reports under the public key lands on the account.
func TestAwgKeyIsMadeOnceAndReachesTheRelay(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	s := New(pool, &fakeBatch{}, nil)
	name := "t-" + uuid.NewString()[:8]
	if _, err := s.Add(ctx, name, unlimited(t, s)); err != nil {
		t.Fatal(err)
	}
	relay, _ := grantRelay(t, pool, entry.ProtoAmneziaWG, name)
	if keys, err := s.KeysOf(ctx, relay, entry.ProtoAmneziaWG); err != nil || len(keys) != 0 {
		t.Fatalf("an account with no key yet is listed: %v, %v", keys, err)
	}

	var account uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM hub_accounts WHERE name = $1`, name).Scan(&account); err != nil {
		t.Fatal(err)
	}
	var first, second accessKey
	if err := s.ensureAwgKey(ctx, account, &first); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureAwgKey(ctx, account, &second); err != nil {
		t.Fatal(err)
	}
	if first.awgPrivate == "" || first != second {
		t.Fatalf("two asks, two answers: %+v, %+v", first, second)
	}
	if !strings.HasPrefix(first.awgAddress, "10.66.") || first.awgAddress == entry.AwgProbeAddress {
		t.Fatalf("address %q", first.awgAddress)
	}

	keys, err := s.KeysOf(ctx, relay, entry.ProtoAmneziaWG)
	if err != nil || len(keys) != 1 {
		t.Fatalf("relay's list: %v, %v", keys, err)
	}
	public, address, ok := strings.Cut(keys[0], " ")
	if !ok || address != first.awgAddress || public == first.awgPrivate {
		t.Fatalf("entry %q: want \"<public key> <address>\"", keys[0])
	}

	// What the other protocols' relays learn is unchanged.
	stream, _ := grantRelay(t, pool, entry.ProtoVlessRealityVision, name)
	if keys, err := s.KeysOf(ctx, stream, entry.ProtoVlessRealityVision); err != nil || len(keys) != 1 || strings.Contains(keys[0], " ") {
		t.Fatalf("a stream relay's list: %v, %v", keys, err)
	}

	boot := strings.Repeat("a", 16)
	if err := s.RecordUsage(ctx, relay, boot, map[string]entry.KeyUsage{public: {Up: 5, Down: 7}}); err != nil {
		t.Fatal(err)
	}
	var up, down int64
	if err := pool.QueryRow(ctx, `SELECT bytes_up, bytes_down FROM hub_key_usage WHERE account_id = $1 AND relay = $2`, account, relay).Scan(&up, &down); err != nil {
		t.Fatalf("usage under the public key did not land on the account: %v", err)
	}
	if up != 5 || down != 7 {
		t.Fatalf("usage %d/%d", up, down)
	}
}
