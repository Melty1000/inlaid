//go:build darwin && !cgo

package capture

import (
	"context"
	"errors"
	"testing"
)

func TestAVFoundationWithoutCgoFailsHonestly(t *testing.T) {
	if _, err := Enumerate(context.Background()); !errors.Is(err, errAVFoundationNeedsCgo) {
		t.Fatalf("Enumerate error = %v", err)
	}
	if _, err := Open(context.Background(), DefaultConfig()); !errors.Is(err, errAVFoundationNeedsCgo) {
		t.Fatalf("Open error = %v", err)
	}
}
