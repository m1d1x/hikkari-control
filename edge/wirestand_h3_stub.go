//go:build !wirestand_h3

package main

import (
	"context"
	"errors"
)

const h3Built = false

// The relay's edge has no cronet: the wire h3's entry is the stand's own
// build (-tags with_purego,wirestand_h3).
func dialStandH3(context.Context, WireStandConfig) (standWire, error) {
	return standWire{}, errors.New("h3: this edge is built without cronet (-tags with_purego,wirestand_h3)")
}

func h3SocketsLine() string { return "" }
