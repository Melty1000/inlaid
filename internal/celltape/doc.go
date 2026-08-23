// Package celltape records the exact terminal-cell states accepted by the UI.
//
// A live producer calls Recorder.PrepareCellFrame, which copies the canonical
// state once into one of a fixed number of reusable queue buffers, then calls
// PreparedCellFrame.Commit only after that state is accepted for display. The
// generic Recorder.Prepare API retains the same deep-copy semantics for other
// producers. Abort releases a reservation without recording it. Commit is the
// acceptance boundary, not a claim that the terminal device has physically
// scanned out the frame: a crash between terminal output and Commit (in either
// order) leaves an unavoidable one-frame cross-device ambiguity.
//
// Each committed state is an independently checksummed chunk ending in a
// commit footer. Recovery stops at the first incomplete or invalid chunk and
// never exposes it. For tapes opened by Create, periodic Sync calls run beside
// the writer on the concurrency-safe os.File so a routine filesystem flush
// stall does not consume the fixed producer queue. Sync requests are coalesced:
// there is at most one in flight, and any Sync error still fails the recorder.
// Caller-owned sinks passed to New keep fully serialized Write/Sync behavior.
// A stalled write or a writer that cannot sustain the producer rate still
// saturates visibly instead of growing memory without bound. Final media
// encoding is intentionally outside this package.
package celltape
