// Command nekko-edge is one Hikkari VPN exit, carried over from Nekko's edge
// relay (embedded Xray). Three protocols, one binary, so the hub deploys and
// rolls them alike:
//
//   - EDGE_PROTO=vless-reality-vision (default, config.go): VLESS + Reality
//     behind an SNI router that hands unknown handshakes to a real cover site;
//   - EDGE_PROTO=vless-xhttp (xhttp.go): an HTTPS site on the relay's own
//     domain with the tunnel under a secret path;
//   - EDGE_PROTO=hysteria2 (voice.go): Hysteria2 over QUIC with masks.
//
// And one that is not a protocol: EDGE_PROTO=cascade (cascade.go), the
// Russian entry of the РФ-каскад, which only hands TCP to an XHTTP exit.
//
// Every protocol leaves through the same exit (egress.go): the internet, minus
// the box's own and the hoster's private networks and outbound SMTP. The edge
// carries bytes and keeps nothing.
//
// An edge is disposable. Its whole identity is the EDGE_* environment (see
// config.go); a box that dies is replaced by the same env on another box.
//
// Usage:
//
//	nekko-edge          run with EDGE_* from the environment
//	nekko-edge check    validate the environment and the rendered Xray config, then exit
//	nekko-edge keygen   print a fresh Reality key pair, short id and client id
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
	"golang.org/x/crypto/curve25519"
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "run":
		// When invoked as `nekko-edge run -c <config.json>`, run raw Xray
		// from that JSON file so cmd/entry-prober can use nekko-edge as its
		// local Xray engine on any provisioned box without a separate xray
		// binary installed on the host.
		if len(os.Args) >= 4 && (os.Args[2] == "-c" || os.Args[2] == "-config") {
			if err := runConfigFile(os.Args[3]); err != nil {
				log.Fatalf("nekko-edge: %v", err)
			}
			return
		}
		if err := run(); err != nil {
			log.Fatalf("nekko-edge: %v", err)
		}
	case "check":
		if osEnv("EDGE_PROTO") == protoCascade {
			if _, err := CascadeConfigFromEnv(osEnv); err != nil {
				log.Fatalf("nekko-edge: %v", err)
			}
			fmt.Println("config OK")
			return
		}
		if _, err := load(); err != nil {
			log.Fatalf("nekko-edge: %v", err)
		}
		fmt.Println("config OK")
	case "keygen":
		if err := keygen(); err != nil {
			log.Fatalf("nekko-edge: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "usage: nekko-edge [run|check|keygen]\n")
		os.Exit(2)
	}
}

// Protocols an edge serves (EDGE_PROTO), spelled as the signed record does.
const (
	protoReality  = "vless-reality-vision"
	protoHysteria = "hysteria2"
	protoXhttp    = "vless-xhttp"
)

// edge is what one EDGE_* environment makes: an Xray config, the loopback
// echo port of its voice admission probe (Reality and Hysteria2 carry
// datagrams; 0 on an XHTTP edge, which carries streams only for now), and for
// an XHTTP edge the HTTPS front (nil otherwise).
type edge struct {
	xray  *core.Config
	echo  int
	front *XhttpConfig
	// proto and userTags say where access keys go (keys.go): the protocol's
	// credential, and the inbounds that let users through.
	proto    string
	userTags []string
}

// load reads the env and builds an Xray config, without starting anything.
func load() (edge, error) {
	var doc map[string]any
	var echo int
	var front *XhttpConfig
	var userTags []string
	proto := osEnv("EDGE_PROTO")
	switch proto {
	case "", protoReality:
		proto = protoReality
		cfg, err := ConfigFromEnv(osEnv)
		if err != nil {
			return edge{}, err
		}
		doc, echo = cfg.XrayConfig(), cfg.EchoPort
		for _, cover := range cfg.Covers {
			userTags = append(userTags, realityTag(cover))
		}
	case protoHysteria:
		cfg, err := VoiceConfigFromEnv(osEnv)
		if err != nil {
			return edge{}, err
		}
		doc, echo = cfg.XrayConfig(), cfg.EchoPort
		userTags = []string{voiceTag}
	case protoXhttp:
		cfg, err := XhttpConfigFromEnv(osEnv)
		if err != nil {
			return edge{}, err
		}
		doc, front = cfg.XrayConfig(), &cfg
		userTags = []string{xhttpTag}
	default:
		return edge{}, fmt.Errorf("EDGE_PROTO: %q is not %s, %s or %s", proto, protoReality, protoHysteria, protoXhttp)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return edge{}, err
	}
	xc, err := core.LoadConfig("json", bytes.NewReader(raw))
	if err != nil {
		return edge{}, fmt.Errorf("xray config: %w", err)
	}
	return edge{xray: xc, echo: echo, front: front, proto: proto, userTags: userTags}, nil
}

func run() error {
	if osEnv("EDGE_PROTO") == protoCascade {
		return cascade()
	}
	e, err := load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e.echo != 0 {
		if err := serveEcho(ctx, e.echo); err != nil {
			return err
		}
	}
	inst, err := core.New(e.xray)
	if err != nil {
		return fmt.Errorf("xray: %w", err)
	}
	if err := inst.Start(); err != nil {
		return fmt.Errorf("xray start: %w", err)
	}
	// The front goes up after Xray: a request through the secret path must
	// find the inbound listening.
	if e.front != nil {
		if err := serveFront(ctx, *e.front); err != nil {
			return fmt.Errorf("front: %w", err)
		}
	}
	// The half-minute self-report to the hub (П9.3), if this box was given
	// one. Started last: it reads counters the running instance owns.
	if pc, ok := pulseFromEnv(osEnv); ok {
		keys, err := newKeyring(inst, e)
		if err != nil {
			return err
		}
		spawnPulse(ctx, inst, pc, keys)
	}

	log.Printf("nekko-edge: up (xray %s)", core.Version())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	return inst.Close()
}

// cascade runs a cascade edge: no Xray, no pulse (it carries no relay and no
// subscriber key), only the forwards.
func cascade() error {
	c, err := CascadeConfigFromEnv(osEnv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runCascade(ctx, c); err != nil {
		return err
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	return nil
}

// runConfigFile starts an Xray instance directly from a JSON configuration
// file (`nekko-edge run -c <path>`), matching `xray run -c <path>`.
func runConfigFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	xc, err := core.LoadConfig("json", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("xray config: %w", err)
	}
	inst, err := core.New(xc)
	if err != nil {
		return fmt.Errorf("xray: %w", err)
	}
	if err := inst.Start(); err != nil {
		return fmt.Errorf("xray start: %w", err)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	return inst.Close()
}

// keygen prints a new edge identity as EDGE_* lines plus the public key the
// client needs. Same encoding as `xray x25519`.
func keygen() error {
	priv := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(priv); err != nil {
		return err
	}
	// Clamp as X25519 does; Xray stores the clamped key.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return err
	}
	sid := make([]byte, 8)
	if _, err := rand.Read(sid); err != nil {
		return err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	id[6] = id[6]&0x0f | 0x40 // version 4
	id[8] = id[8]&0x3f | 0x80 // RFC 4122 variant
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])

	enc := base64.RawURLEncoding
	fmt.Printf("EDGE_PRIVATE_KEY=%s\n", enc.EncodeToString(priv))
	fmt.Printf("EDGE_SHORT_IDS=%s\n", hex.EncodeToString(sid))
	fmt.Printf("EDGE_CLIENTS=%s\n", uuid)
	fmt.Printf("# client side: public_key=%s\n", enc.EncodeToString(pub))
	return nil
}
