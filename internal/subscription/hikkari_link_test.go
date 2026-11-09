package subscription

import (
	"net/http"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

func TestHikkariLink(t *testing.T) {
	rec := entry.RelayRecord{Name: "nvn1", Proto: entry.ProtoHikkari, Address: "179.253.75.148", Port: 443,
		HikkariParams: &entry.HikkariParams{Host: "vm.example", User: "probe", Pass: "0123456789abcdef0123"}}
	rec = withKey(rec, accessKey{hy2Auth: "subscriber-key-0123456789"})
	link, err := HikkariLink(rec, false)
	if err != nil {
		t.Fatal(err)
	}
	// Without the hikkari-dup feature the link is v1.01's: clients up to
	// 1.0.69 refuse a parameter they do not know.
	if want := "hikkari+v1://hikkari:subscriber-key-0123456789@179.253.75.148:443?sni=vm.example#Hikkari%20nvn1"; link != want {
		t.Fatalf("link\n got %s\nwant %s", link, want)
	}
	if plain, err := Link(rec); err != nil || plain != link {
		t.Fatal("Link must render a Hikkari relay as its hikkari+v1 link")
	}
	if dup, err := HikkariLink(rec, true); err != nil || dup != "hikkari+v1://hikkari:subscriber-key-0123456789@179.253.75.148:443?dup=1&sni=vm.example#Hikkari%20nvn1" {
		t.Fatalf("v1.02 link %s %v", dup, err)
	}
	naive := entry.RelayRecord{Name: "nvn2", Proto: entry.ProtoNaive, Address: "179.253.75.149", Port: 443,
		NaiveParams: &entry.NaiveParams{Host: "vm.example", User: "probe", Pass: "0123456789abcdef0123"}}
	if _, err := HikkariLink(naive, false); err == nil {
		t.Fatal("a Naive relay got a hikkari link")
	}
	h := http.Header{}
	h.Set(featuresHeader, "naive, hikkari, hikkari-dup")
	if !hasFeature(h, "hikkari") || !hasFeature(h, "hikkari-dup") || !hasNaiveFeature(h) || hasFeature(h, "awg") {
		t.Fatal("features")
	}
}
