//go:build !windows && !linux && !darwin

package capture

import (
	"context"
	"errors"
)

func Enumerate(context.Context) ([]Device, error) {
	return nil, errors.New("native camera capture is unavailable on this operating system")
}

func Open(context.Context, Config) (*Session, error) {
	return nil, errors.New("native camera capture is unavailable on this operating system")
}
