//go:build wirestand_h3 && with_purego

package main

import (
	"github.com/m1d1x/hikkari-protocol/trunk/chromium"
	"github.com/sagernet/cronet-go"
)

// loadStandCronet loads libcronet.so: from path (WIRESTAND_CRONET, where the
// hub stages it beside the stand), else wherever cronet looks.
func loadStandCronet(path string) error {
	if path != "" {
		return cronet.LoadLibrary(path)
	}
	return chromium.Load()
}
