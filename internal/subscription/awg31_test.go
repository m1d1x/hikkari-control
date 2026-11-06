package subscription

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

const awg31TestKey = "pBNbQ3vIFMTZCbCkQtLObNUtsM9hAi+ZhlrgZbH0Xgc="

func awg31Record() entry.RelayRecord {
	rec := awgRecord()
	p := *rec.AwgParams
	p.S1, p.S2, p.S3, p.S4 = 16, 17, 18, 19
	p.H1, p.H2, p.H3, p.H4 = "1", "2", "3", "4"
	p.AwgV3 = entry.AwgV3{
		HeaderProtectionKey: awg31TestKey, ContentPaddingAddition: "10-50",
		RekeyAfterTime: "100-120", RekeyTimeout: "5-7", RejectAfterTime: "170-190",
		KeepaliveTimeout: "10-15", MaxHandshakeAttempts: "15-20",
		RandomTrailers: true, DisableCookies: true,
	}
	rec.AwgParams = &p
	return rec
}

func awgLinkConf(t *testing.T, link string) string {
	t.Helper()
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
	var got struct {
		Containers []struct {
			Awg struct {
				LastConfig string `json:"last_config"`
			} `json:"awg"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	return got.Containers[0].Awg.LastConfig
}

// A 3.1 relay's link names every 3.1 field as the client's parser reads them
// (hikkari-client engine/core/profile/legacy/awg3.go); a 2.0 relay's, none.
func TestAwg31LinkCarriesEveryField(t *testing.T) {
	link, err := AwgLink(awg31Record())
	if err != nil {
		t.Fatal(err)
	}
	conf := awgLinkConf(t, link)
	for _, want := range []string{
		"HeaderProtectionKey = " + awg31TestKey, "ContentPaddingAddition = 10-50", "RekeyAfterTime = 100-120",
		"RekeyTimeout = 5-7", "RejectAfterTime = 170-190", "KeepaliveTimeout = 10-15",
		"MaxHandshakeAttempts = 15-20", "RandomTrailers = 1", "DisableCookies = 1", "H1 = 1",
	} {
		if !strings.Contains(conf, "\n"+want+"\n") {
			t.Errorf(".conf lacks %q:\n%s", want, conf)
		}
	}
	old, err := AwgLink(awgRecord())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"HeaderProtection", "ContentPadding", "Rekey", "RejectAfter", "KeepaliveTimeout", "MaxHandshake", "RandomTrailers", "DisableCookies"} {
		if strings.Contains(awgLinkConf(t, old), k) {
			t.Errorf("a 2.0 link carries %s", k)
		}
	}
}

// A 3.1 relay goes only to a client that says it carries 3.1: 1.0.52 refuses
// the link, and an older client drops the fields and gets no answer.
func TestAwg31RelayGoesOnlyToA31Client(t *testing.T) {
	v20, v31 := awgRecord(), awg31Record()
	for features, want := range map[string][2]bool{
		"":                         {false, false},
		"amneziawg":                {true, false},
		"amneziawg, amneziawg-3.1": {true, true},
		"amneziawg-3.1":            {false, false},
	} {
		h := http.Header{}
		if features != "" {
			h.Set(featuresHeader, features)
		}
		c := wantsAwg(h)
		if got := [2]bool{c.dials(v20), c.dials(v31)}; got != want {
			t.Errorf("features %q: dials 2.0/3.1 = %v, want %v", features, got, want)
		}
	}
}
