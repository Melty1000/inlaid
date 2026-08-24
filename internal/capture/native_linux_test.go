//go:build linux && cgo

package capture

import "testing"

func TestNativeMapBudgetRejectsOversizedQueriedLengths(t *testing.T) {
	tests := []struct {
		name       string
		lengths    []uint64
		maxBuffer  uint64
		maxMapped  uint64
		wantMapped uint64
	}{
		{
			name:       "single buffer",
			lengths:    []uint64{4096, 4097},
			maxBuffer:  4096,
			maxMapped:  12 * 1024,
			wantMapped: 4096,
		},
		{
			name:       "cumulative buffers",
			lengths:    []uint64{4096, 4096, 1},
			maxBuffer:  4096,
			maxMapped:  8192,
			wantMapped: 8192,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped, err := nativeMapBudget(test.lengths, test.maxBuffer, test.maxMapped)
			if err == nil {
				t.Fatal("oversized queried buffer lengths were accepted")
			}
			if mapped != test.wantMapped {
				t.Fatalf("reserved bytes = %d, want %d before rejection", mapped, test.wantMapped)
			}
		})
	}
}

func TestNativeMapBudgetAcceptsExactLimits(t *testing.T) {
	mapped, err := nativeMapBudget([]uint64{4096, 4096}, 4096, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if mapped != 8192 {
		t.Fatalf("reserved bytes = %d, want 8192", mapped)
	}
}
