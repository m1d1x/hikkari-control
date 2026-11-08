package entry

import (
	"math"
	"strconv"
	"strings"
)

// rate is a throughput held in Mbit/s as a person reads it: the unit follows
// the size (кбит/с below one megabit, Гбит/с from a thousand), three
// significant digits, a decimal comma. 0.15 Mbit/s is "150 кбит/с", not
// "0.15 Мбит/с"; the admin's formatRate (admin/src/lib/utils/rate.ts) prints
// the same.
func rate(mbps float64) string {
	return rateSigned(mbps, false)
}

// rateDelta is a change of throughput: the same as rate, with its sign always
// written ("+1,2 Мбит/с", "−300 кбит/с").
func rateDelta(mbps float64) string {
	return rateSigned(mbps, true)
}

func rateSigned(mbps float64, sign bool) string {
	if math.IsNaN(mbps) || math.IsInf(mbps, 0) {
		return "- Мбит/с"
	}
	v, unit := math.Abs(mbps), "Мбит/с"
	switch {
	case v == 0:
	case v < 1:
		v, unit = v*1000, "кбит/с"
	case v >= 1000:
		v, unit = v/1000, "Гбит/с"
	}
	digits := 0
	switch {
	case v == 0 || v >= 100:
	case v >= 10:
		digits = 1
	default:
		digits = 2
	}
	s := strconv.FormatFloat(v, 'f', digits, 64)
	if digits > 0 {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	// Rounding can carry into the next unit: 999.6 кбит/с reads 1 Мбит/с.
	if s == "1000" && unit != "Гбит/с" {
		s, unit = "1", map[string]string{"кбит/с": "Мбит/с", "Мбит/с": "Гбит/с"}[unit]
	}
	s = strings.Replace(s, ".", ",", 1)
	switch {
	case s == "0":
	case mbps < 0:
		s = "−" + s
	case sign:
		s = "+" + s
	}
	return s + " " + unit
}
