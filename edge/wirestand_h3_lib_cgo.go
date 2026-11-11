//go:build wirestand_h3 && !with_purego

package main

// loadStandCronet is nothing to do: libcronet is linked in (tests on macOS).
func loadStandCronet(string) error { return nil }
