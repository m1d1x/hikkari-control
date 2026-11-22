//go:build !linux

package hoptrace

import (
	"context"
	"errors"
)

func Capture(context.Context, string) error { return errors.New("hop trace capture requires Linux") }
