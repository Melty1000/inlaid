package mfcapture

import "testing"

func TestPacketPoolReusesStorageWithinHardBound(t *testing.T) {
	pool := newPacketPool(16 << 10)
	first, err := pool.acquire(3000)
	if err != nil {
		t.Fatal(err)
	}
	owner := first.owner
	data := &first.Data[0]
	first.Release()

	second, err := pool.acquire(3000)
	if err != nil {
		t.Fatal(err)
	}
	if second.owner != owner || &second.Data[0] != data {
		t.Fatal("packet pool did not reuse the released byte bucket")
	}
	second.Release()
	if pool.reserved > pool.maxBytes {
		t.Fatalf("packet pool reserved %d bytes past its %d-byte bound", pool.reserved, pool.maxBytes)
	}
}

func TestPacketPoolRejectsExhaustionWithoutAllocatingPastBound(t *testing.T) {
	pool := newPacketPool(8 << 10)
	held, err := pool.acquire(5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.acquire(1); err == nil {
		t.Fatal("exhausted packet pool accepted storage past its hard bound")
	}
	if pool.reserved != 8<<10 {
		t.Fatalf("packet pool reserved = %d, want exactly 8192", pool.reserved)
	}
	held.Release()
}

func TestStalePacketReleaseCannotReturnANewerLease(t *testing.T) {
	pool := newPacketPool(8 << 10)
	stale, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	stale.Release()
	current, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	stale.Release()
	if got := current.owner.token.Load(); got != current.ownerToken {
		t.Fatalf("stale release cleared current lease token: got %d want %d", got, current.ownerToken)
	}
	current.Release()
}

func TestPacketPoolCloseDropsFreeStorageAndRejectsNewLeases(t *testing.T) {
	pool := newPacketPool(8 << 10)
	packet, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	packet.Release()
	pool.close()
	if pool.reserved != 0 {
		t.Fatalf("closed packet pool retained %d free bytes", pool.reserved)
	}
	if _, err := pool.acquire(100); err == nil {
		t.Fatal("closed packet pool accepted a new lease")
	}
}

func TestPacketPoolSteadyLeaseHasNoAllocations(t *testing.T) {
	pool := newPacketPool(8 << 10)
	warm, err := pool.acquire(100)
	if err != nil {
		t.Fatal(err)
	}
	warm.Release()
	allocations := testing.AllocsPerRun(1000, func() {
		packet, acquireErr := pool.acquire(100)
		if acquireErr != nil {
			panic(acquireErr)
		}
		packet.Release()
	})
	if allocations != 0 {
		t.Fatalf("steady packet lease allocations = %.2f, want 0", allocations)
	}
}
