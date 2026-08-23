//go:build !windows

package mfcapture

import (
	"context"
	"errors"
)

func Enumerate(context.Context) ([]Device, error) {
	return nil, errors.New("Media Foundation capture requires Windows")
}

func Open(context.Context, Config) (*Session, error) {
	return nil, errors.New("Media Foundation capture requires Windows")
}
