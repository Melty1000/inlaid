//go:build darwin && !cgo

package capture

import (
	"context"
	"errors"
)

var errAVFoundationNeedsCgo = errors.New("native macOS camera capture requires a cgo-enabled build with Apple Clang and the macOS SDK")

func Enumerate(ctx context.Context) ([]Device, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, errAVFoundationNeedsCgo
}

func Open(ctx context.Context, _ Config) (*Session, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, errAVFoundationNeedsCgo
}
