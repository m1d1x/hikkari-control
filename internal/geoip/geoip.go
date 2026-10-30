// Package geoip places an IP address: country and city from a local MMDB
// file (DB-IP City Lite or MaxMind GeoLite2-City — same format), and the
// honest «local network» for an address no database can place.
package geoip

import (
	"net"
	"regexp"

	// странно почему у нас испорт идёт с гитхаба, а не локально скачанное, или оно скачано?
	"github.com/oschwald/geoip2-golang"
)

// Result holds parsed geo data for an IP address.
type Result struct {
	CountryCode string
	CountryName string
	Region      string
	City        string
	// Local is set for an address that never leaves a private network
	// (loopback, RFC 1918, CGNAT, link-local, ULA). No database places it,
	// and it is a fact rather than a miss: the device is on the same network
	// as the server — a dev stand, or a misconfigured proxy.
	Local bool
}

// Lookup wraps a GeoIP2-City-format database reader.
// A nil *Lookup is safe to use — Lookup() still recognises local addresses
// and returns no place for the rest.
type Lookup struct {
	db *geoip2.Reader
}

// New opens a City MMDB file. Returns (nil, nil) if dbPath is empty.
func New(dbPath string) (*Lookup, error) {
	if dbPath == "" {
		return nil, nil
	}
	db, err := geoip2.Open(dbPath)
	if err != nil {
		return nil, err
	}
	return &Lookup{db: db}, nil
}

// Close releases the database reader.
func (l *Lookup) Close() error {
	if l == nil || l.db == nil {
		return nil
	}
	return l.db.Close()
}

// Lookup returns geo info for ip. Safe to call on nil receiver.
//
// The city and the country are named in ONE language: Russian when the
// database has it for both (the audience is RU), English otherwise. DB-IP Lite
// names countries in Russian but cities only in English, and «Moscow, Россия»
// reads as a bug.
func (l *Lookup) Lookup(ip string) Result {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return Result{}
	}
	if IsLocal(parsed) {
		return Result{Local: true}
	}
	if l == nil || l.db == nil {
		return Result{}
	}
	record, err := l.db.City(parsed)
	if err != nil {
		return Result{}
	}
	return place(record)
}

func place(record *geoip2.City) Result {
	var region map[string]string
	if len(record.Subdivisions) > 0 {
		region = record.Subdivisions[0].Names
	}
	lang := "en"
	if has(record.Country.Names, "ru") && (len(record.City.Names) == 0 || has(record.City.Names, "ru")) {
		lang = "ru"
	}
	return Result{
		CountryCode: record.Country.IsoCode,
		CountryName: name(record.Country.Names, lang),
		Region:      name(region, lang),
		City:        cityName(name(record.City.Names, lang)),
	}
}

// cgnat is RFC 6598 shared address space, which net.IP.IsPrivate leaves out.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// IsLocal reports whether ip is an address of a private network rather than
// of the internet.
func IsLocal(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || cgnat.Contains(ip)
}

func has(names map[string]string, lang string) bool { return names[lang] != "" }

// name returns the name in lang, falling back to English.
func name(names map[string]string, lang string) string {
	if v := names[lang]; v != "" {
		return v
	}
	return names["en"]
}

// districtSuffix is DB-IP's district qualifier: «Moscow (Tsentralnyy
// administrativnyy okrug)». The district is noise on a «where is this device»
// line; the city is what a person recognises.
var districtSuffix = regexp.MustCompile(`\s*\([^)]*\)$`)

func cityName(s string) string { return districtSuffix.ReplaceAllString(s, "") }
