package downdetector

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// now is aligned so window [now-300, now) is the latest complete one.
var testNow = time.Unix(1761800400, 0)

const validReport = `{
  "install": "r-0123456789abcdef01234567",
  "asn": 12389,
  "country": "ru",
  "region": "moscow",
  "window": [1761800100, 1761800400],
  "servers": [
    {"id": "s:7a19192edd834db17881c0ad69c22fa9", "proto": "vless-reality-xhttp", "src": "tunnel", "ok": 9, "fail": 1, "rtt_p50": 74}
  ]
}`

func TestDecodeReportAcceptsTheClientExample(t *testing.T) {
	r, err := DecodeReport(strings.NewReader(validReport), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if r.ASN != 12389 || len(r.Servers) != 1 || r.Servers[0].OK != 9 {
		t.Fatalf("decoded %+v", r)
	}
}

func TestDecodeReportAcceptsUnknownNetwork(t *testing.T) {
	body := strings.Replace(validReport, `"asn": 12389`, `"asn": 0`, 1)
	body = strings.Replace(body, `"country": "ru"`, `"country": ""`, 1)
	body = strings.Replace(body, `"region": "moscow"`, `"region": ""`, 1)
	if _, err := DecodeReport(strings.NewReader(body), testNow); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeReportRejects(t *testing.T) {
	servers := make([]string, MaxServers+1)
	for i := range servers {
		servers[i] = fmt.Sprintf(`{"id":"n:node-%d","proto":"vless","src":"tunnel","ok":1,"fail":0,"rtt_p50":1}`, i)
	}
	tooMany := fmt.Sprintf(`{"install":"r-0123456789abcdef01234567","asn":1,"country":"ru","region":"","window":[1761800100,1761800400],"servers":[%s]}`, strings.Join(servers, ","))

	cases := map[string]string{
		"unknown field":         strings.Replace(validReport, `"asn"`, `"ip": "1.2.3.4", "asn"`, 1),
		"unknown server field":  strings.Replace(validReport, `"rtt_p50"`, `"host": "x", "rtt_p50"`, 1),
		"body over 64 KiB":      strings.Replace(validReport, `"moscow"`, `"`+strings.Repeat("a", MaxBodyBytes)+`"`, 1),
		"too many servers":      tooMany,
		"window not aligned":    strings.Replace(validReport, `[1761800100, 1761800400]`, `[1761800101, 1761800401]`, 1),
		"window not 300 s":      strings.Replace(validReport, `[1761800100, 1761800400]`, `[1761800100, 1761800700]`, 1),
		"window in the future":  strings.Replace(validReport, `[1761800100, 1761800400]`, `[1761800400, 1761800700]`, 1),
		"window older than 2 h": strings.Replace(validReport, `[1761800100, 1761800400]`, `[1761792900, 1761793200]`, 1),
		"bad src":               strings.Replace(validReport, `"tunnel"`, `"udp"`, 1),
		"bad id":                strings.Replace(validReport, `s:7a19192edd834db17881c0ad69c22fa9`, `s:example.com`, 1),
		"bad node id":           strings.Replace(validReport, `s:7a19192edd834db17881c0ad69c22fa9`, `n:Fra_1`, 1),
		"country not 2 letters": strings.Replace(validReport, `"ru"`, `"rus"`, 1),
		"country upper case":    strings.Replace(validReport, `"ru"`, `"RU"`, 1),
		"region not a slug":     strings.Replace(validReport, `"moscow"`, `"Moscow City"`, 1),
		"region too long":       strings.Replace(validReport, `"moscow"`, `"`+strings.Repeat("a", 65)+`"`, 1),
		"bad install":           strings.Replace(validReport, `r-0123456789abcdef01234567`, `device-42`, 1),
		"negative ok":           strings.Replace(validReport, `"ok": 9`, `"ok": -1`, 1),
		"no samples":            strings.Replace(strings.Replace(validReport, `"ok": 9`, `"ok": 0`, 1), `"fail": 1`, `"fail": 0`, 1),
		"duplicate server/src":  strings.Replace(validReport, `"rtt_p50": 74}`, `"rtt_p50": 74}, {"id": "s:7a19192edd834db17881c0ad69c22fa9", "proto": "vless-reality-xhttp", "src": "tunnel", "ok": 1, "fail": 0, "rtt_p50": 1}`, 1),
		"trailing data":         validReport + `{}`,
		"not json":              `install=r-1`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeReport(strings.NewReader(body), testNow); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
