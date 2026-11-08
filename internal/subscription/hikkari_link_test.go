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
	link, err := HikkariLink(rec)
	if err != nil {
		t.Fatal(err)
	}
	if want := "hikkari+v1://hikkari:subscriber-key-0123456789@179.253.75.148:443?sni=vm.example#Hikkari%20nvn1"; link != want {
		t.Fatalf("link\n got %s\nwant %s", link, want)
	}
	if plain, err := Link(rec); err != nil || plain != link {
		t.Fatal("Link must render a Hikkari relay as its hikkari+v1 link")
	}
	naive := entry.RelayRecord{Name: "nvn2", Proto: entry.ProtoNaive, Address: "179.253.75.149", Port: 443,
		NaiveParams: &entry.NaiveParams{Host: "vm.example", User: "probe", Pass: "0123456789abcdef0123"}}
	if _, err := HikkariLink(naive); err == nil {
		t.Fatal("a Naive relay got a hikkari link")
	}
	h := http.Header{}
	h.Set(featuresHeader, "naive, hikkari")
	if !hasFeature(h, "hikkari") || !hasNaiveFeature(h) || hasFeature(h, "awg") {
		t.Fatal("features")
	}
}
