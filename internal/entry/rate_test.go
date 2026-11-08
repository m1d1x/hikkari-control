package entry

import (
	"math"
	"testing"
)

func TestRate(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{
		{0, "0 Мбит/с"},
		{0.15, "150 кбит/с"},
		{0.003, "3 кбит/с"},
		{0.0123, "12,3 кбит/с"},
		{0.0004, "0,4 кбит/с"},
		{0.9996, "1 Мбит/с"},
		{1, "1 Мбит/с"},
		{2.4, "2,4 Мбит/с"},
		{2.456, "2,46 Мбит/с"},
		{15.34, "15,3 Мбит/с"},
		{63, "63 Мбит/с"},
		{150.4, "150 Мбит/с"},
		{999.7, "1 Гбит/с"},
		{1250, "1,25 Гбит/с"},
		{-0.3, "−300 кбит/с"},
		{math.NaN(), "— Мбит/с"},
	} {
		if got := rate(c.in); got != c.want {
			t.Errorf("rate(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRateDelta(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{
		{1.2, "+1,2 Мбит/с"},
		{-0.3, "−300 кбит/с"},
		{0, "0 Мбит/с"},
	} {
		if got := rateDelta(c.in); got != c.want {
			t.Errorf("rateDelta(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
