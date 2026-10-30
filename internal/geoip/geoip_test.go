package geoip

import (
	"net"
	"testing"

	"github.com/oschwald/geoip2-golang"
)

func TestLocalAddressesAreLocalWithoutADatabase(t *testing.T) {
	var l *Lookup // no database: a local address is still a fact
	for _, ip := range []string{
		"127.0.0.1", "::1",
		"10.1.2.3", "172.20.10.12", "192.168.65.1", // RFC 1918; Docker Desktop's NAT
		"100.64.0.1", "100.127.255.254", // CGNAT
		"169.254.1.1", "fe80::1", "fd00::1", "0.0.0.0",
	} {
		if got := l.Lookup(ip); !got.Local || got.CountryCode != "" {
			t.Errorf("%s: %+v, want Local", ip, got)
		}
	}
	for _, ip := range []string{"8.8.8.8", "89.223.127.23", "100.128.0.1", "2a00:1450::1", "", "nonsense"} {
		if got := l.Lookup(ip); got != (Result{}) {
			t.Errorf("%s: %+v, want nothing", ip, got)
		}
	}
}

func city(country, cityNames map[string]string) *geoip2.City {
	var r geoip2.City
	r.Country.IsoCode = "RU"
	r.Country.Names = country
	r.City.Names = cityNames
	return &r
}

func TestPlaceNamesCityAndCountryInOneLanguage(t *testing.T) {
	ruCountry := map[string]string{"en": "Russia", "ru": "Россия"}
	for _, tc := range []struct {
		name          string
		record        *geoip2.City
		city, country string
	}{
		{
			// DB-IP Lite: the country has Russian, the city does not.
			"city only in English",
			city(ruCountry, map[string]string{"en": "St Petersburg"}),
			"St Petersburg", "Russia",
		},
		{
			"both in Russian",
			city(ruCountry, map[string]string{"en": "Moscow", "ru": "Москва"}),
			"Москва", "Россия",
		},
		{
			"no city at all",
			city(ruCountry, nil),
			"", "Россия",
		},
		{
			"district qualifier dropped",
			city(ruCountry, map[string]string{"en": "Moscow (Tsentralnyy administrativnyy okrug)"}),
			"Moscow", "Russia",
		},
	} {
		got := place(tc.record)
		if got.City != tc.city || got.CountryName != tc.country || got.CountryCode != "RU" || got.Local {
			t.Errorf("%s: %+v, want %q, %q", tc.name, got, tc.city, tc.country)
		}
	}
}

func TestIsLocalMatchesLookup(t *testing.T) {
	if IsLocal(net.ParseIP("1.1.1.1")) || !IsLocal(net.ParseIP("192.168.0.1")) {
		t.Fatal("IsLocal disagrees with the address classes")
	}
}
