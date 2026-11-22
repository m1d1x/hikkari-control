// Package hoptrace correlates samples of encrypted UDP packets across the
// cascade. A packet's payload survives DNAT and masquerade. The capture keeps
// only a fingerprint, a timestamp and a label in a short, bounded buffer.
package hoptrace

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const Retention = 360

const configVersion = 2

var validKey = regexp.MustCompile(`^[a-f0-9]{16}$`)

type Peer struct {
	Name string
	IP   netip.Addr
}

type Config struct {
	Version uint8
	Key     string
	Peers   []Peer
	Ports   []uint16
}

func (c Config) Encode() string {
	c.Version = configVersion
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func Decode(value string) (Config, error) {
	var c Config
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Version != configVersion || !validKey.MatchString(c.Key) || len(c.Peers) == 0 || len(c.Peers) > 16 || len(c.Ports) == 0 || len(c.Ports) > 16 {
		return c, fmt.Errorf("invalid hop trace configuration")
	}
	for _, p := range c.Peers {
		if !p.IP.Is4() || p.Name == "" {
			return c, fmt.Errorf("invalid hop trace peer")
		}
	}
	return c, nil
}

func Dir(key string) string { return "/run/hikkari-hoptrace-" + key }

type Packet struct {
	At   int64  // Unix milliseconds
	ID   uint64 // hash of UDP length and the start of the encrypted payload
	Port uint16 // the destination port upstream and source port downstream
	Peer uint8  // index into the capture configuration
	Out  bool
}

type Bucket struct {
	Second       int64
	Complete     bool
	Drops        uint32
	Overflow     bool
	SamplingBits uint8
	Packets      []Packet
}

// Sampled is shared by the observers and the correlator. A bucket may keep a
// thinner sample under load, but any two observers still share the subset
// defined by the larger sampling value.
func Sampled(id uint64, bits uint8) bool {
	return id&((uint64(1)<<bits)-1) == 0
}

const maxSamplingBits = 16

func (b *Bucket) record(p Packet) {
	for Sampled(p.ID, b.SamplingBits) {
		if len(b.Packets) < cap(b.Packets) {
			b.Packets = append(b.Packets, p)
			return
		}
		if b.SamplingBits == maxSamplingBits {
			b.Overflow = true
			return
		}
		b.SamplingBits++
		kept := b.Packets[:0]
		for _, sample := range b.Packets {
			if Sampled(sample.ID, b.SamplingBits) {
				kept = append(kept, sample)
			}
		}
		b.Packets = kept
	}
}

type Window struct {
	Config  string
	Buckets []Bucket
}

func bucketPath(dir string, second int64) string {
	return filepath.Join(dir, fmt.Sprintf("%03d.json", second%Retention))
}

// Write replaces a slot atomically while the hub may be reading it.
func Write(dir, config string, b Bucket) error {
	f := bucketPath(dir, b.Second)
	raw, err := json.Marshal(Window{Config: config, Buckets: []Bucket{b}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(f+".new", raw, 0600); err != nil {
		return err
	}
	return os.Rename(f+".new", f)
}

// Read reports the available seconds, including complete seconds with no
// packets. Expired slots and samples under a different topology are distinct
// from observed silence.
func Read(config string, from, until int64) (Window, error) {
	w := Window{Config: config}
	c, err := Decode(config)
	if err != nil {
		return w, err
	}
	if from < 0 || until <= from || until-from > 30 {
		return w, fmt.Errorf("invalid hop trace window")
	}
	for second := from; second < until; second++ {
		raw, err := os.ReadFile(bucketPath(Dir(c.Key), second))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return w, err
		}
		var sample Window
		if err := json.Unmarshal(raw, &sample); err != nil {
			return w, err
		}
		if sample.Config == config && len(sample.Buckets) == 1 && sample.Buckets[0].Second == second {
			w.Buckets = append(w.Buckets, sample.Buckets[0])
		}
	}
	return w, nil
}

const Lease = 5 * time.Minute
