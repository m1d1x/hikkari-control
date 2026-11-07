package stand

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/net/dns/dnsmessage"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

func TestCapacityAWGConfigRequiresTunnelDNS(t *testing.T) {
	endpoint := map[string]any{"type": "wireguard", "tag": "original"}
	if _, _, err := Config(endpoint, func(map[string]any, string) error { return nil }, ""); err == nil {
		t.Fatal("endpoint without tunnel DNS accepted")
	}
	cfg, _, err := Config(endpoint, func(map[string]any, string) error { return nil }, "", map[string]any{"tag": "dns-in-probe", "type": "udp", "server": "1.1.1.1", "detour": "probe"})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(cfg, "__PORT__", "12345")), &document); err != nil {
		t.Fatal(err)
	}
	if document["outbounds"] != nil || document["endpoints"] == nil || document["dns"] == nil || endpoint["tag"] != "original" {
		t.Fatal("wrong or mutating endpoint config")
	}
	if Applies("fp", endpoint) || Applies("pacing", endpoint) {
		t.Fatal("TLS matrix accepted AWG")
	}
}

// Actual AWG device and actual shipped core, entirely on loopback and a
// userspace IP stack. No external test service, user VPN or system TUN.
func TestCapacityAWGNativeTunnel(t *testing.T) {
	core := os.Getenv("HIKKARI_TEST_CORE")
	if core == "" {
		t.Skip("set HIKKARI_TEST_CORE to the AWG-capable sing-box binary")
	}
	for _, v3 := range []bool{false, true} {
		t.Run(fmt.Sprintf("v3=%v", v3), func(t *testing.T) {
			serverPrivate, serverPublic, err := entry.NewAwgKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			clientPrivate, clientPublic, err := entry.NewAwgKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			hexKey := func(key string) string {
				raw, err := base64.StdEncoding.DecodeString(key)
				if err != nil {
					t.Fatal(err)
				}
				return hex.EncodeToString(raw)
			}
			socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := socket.LocalAddr().(*net.UDPAddr).Port
			socket.Close()
			tun, stack, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.66.0.1")}, nil, 1280)
			if err != nil {
				t.Fatal(err)
			}
			dev := device.NewDevice(tun, conn.NewDefaultBind(), &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf})
			defer dev.Close()
			settings := "jc=4\njmin=40\njmax=70\ns1=33\ns2=57\ns3=21\ns4=16\nh1=11\nh2=12\nh3=13\nh4=14\n"
			endpoint := map[string]any{"type": "wireguard", "tag": "probe", "system": false, "address": []any{"10.66.0.2/32"}, "private_key": clientPrivate, "mtu": 1280, "peers": []any{map[string]any{"address": "127.0.0.1", "port": port, "public_key": serverPublic, "allowed_ips": []any{"0.0.0.0/0"}}}, "jc": 4, "jmin": 40, "jmax": 70, "s1": 33, "s2": 57, "s3": 21, "s4": 16, "h1": "11", "h2": "12", "h3": "13", "h4": "14", "destination_resolver": map[string]any{"server": "dns-in-probe", "strategy": "ipv4_only"}}
			if v3 {
				settings += "header_protection_key=" + hexKey(serverPrivate) + "\ncontent_padding_addition=10-50\nrandom_trailers=true\n"
				endpoint["header_protection_key"] = serverPrivate
				endpoint["content_padding_addition"] = "10-50"
				endpoint["random_trailers"] = true
			}
			config := "private_key=" + hexKey(serverPrivate) + "\nlisten_port=" + strconv.Itoa(port) + "\n" + settings + "public_key=" + hexKey(clientPublic) + "\nallowed_ip=10.66.0.2/32\n"
			if err := dev.IpcSet(config); err != nil {
				t.Fatal(err)
			}
			if err := dev.Up(); err != nil {
				t.Fatal(err)
			}
			dnsSocket, err := stack.ListenUDP(&net.UDPAddr{IP: net.ParseIP("10.66.0.1"), Port: 53})
			if err != nil {
				t.Fatal(err)
			}
			defer dnsSocket.Close()
			var queries atomic.Int32
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, addr, err := dnsSocket.ReadFrom(buffer)
					if err != nil {
						return
					}
					var parser dnsmessage.Parser
					header, err := parser.Start(buffer[:n])
					if err != nil {
						continue
					}
					question, err := parser.Question()
					if err != nil {
						continue
					}
					queries.Add(1)
					builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, RecursionDesired: header.RecursionDesired, RecursionAvailable: true})
					builder.StartQuestions()
					builder.Question(question)
					builder.StartAnswers()
					if question.Type == dnsmessage.TypeA {
						builder.AResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{10, 66, 0, 1}})
					}
					response, err := builder.Finish()
					if err == nil {
						dnsSocket.WriteTo(response, addr)
					}
				}
			}()
			listener, err := stack.ListenTCP(&net.TCPAddr{IP: net.ParseIP("10.66.0.1"), Port: 8080})
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rtt" {
					w.WriteHeader(204)
					return
				}
				size, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
				w.Header().Set("Content-Length", strconv.Itoa(size))
				buf := make([]byte, 16<<10)
				for size > 0 {
					n := min(size, len(buf))
					if _, err := w.Write(buf[:n]); err != nil {
						return
					}
					size -= n
					time.Sleep(2 * time.Millisecond)
				}
			})}
			go server.Serve(listener)
			defer server.Close()
			profile := Profile{CapacityKey: strings.Repeat("a", 64), Config: endpoint, Resolver: map[string]any{"tag": "dns-in-probe", "type": "udp", "server": "10.66.0.1", "detour": "probe"}}
			runner := Runner{Kit: Local{Core: core}}
			sample := runner.measureProfileCapacityAt(context.Background(), profile, true, "http://capacity.awg.test:8080/download", "http://capacity.awg.test:8080/rtt")
			if sample.Error != "" || sample.Mbps <= 0 || sample.RTTMS <= 0 {
				t.Fatalf("native AWG failed: %+v", sample)
			}
			if queries.Load() == 0 {
				t.Fatal("destination DNS did not traverse the AWG tunnel")
			}
			if err := sample.Validate(time.Now()); err != nil {
				t.Fatal(err)
			}
			t.Logf("actual AWG measurement: %.2f Mbps, RTT %d ms, bytes %d/%d", sample.Mbps, sample.RTTMS, sample.Bytes, capacity.MaxBytes)
		})
	}
}
