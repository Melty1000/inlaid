# Phase 1: Core refinement

Status: accepted and complete.

## Contract

Phase 1 makes the current Windows beta smaller, faster, safer, and easier to
read without changing its picture, controls, recording formats, or terminal
requirements.

The canonical `CellFrame` remains the only image state shared by preview,
snapshots, CellTape, and export. Camera callbacks stay short. Every asynchronous
handoff remains bounded and has one clear owner. Full-canvas work stays off the
live path.

## Work

1. Remove work and API surface that no production or test path uses.
2. Preserve exact cell output while reducing solver and YCbCr reduction cost.
3. Stop reparsing trusted live ANSI solely to recover dimensions already known
   by the renderer.
4. Make CellTape memory follow observed queue pressure instead of configured
   capacity, and reduce normal export from three full tape decodes to two.
5. Make recovery cancellable, apply the same tape limits as recording, reject
   hostile timeline amplification before FFmpeg, and never retire a tape from
   a filename collision alone.
6. Verify FFmpeg candidates before a recording starts.
7. Keep Mosaic only if measurement confirms that its demo-only value earns its
   dependency and does not tax the live camera path.

## Evidence

The implementation is accepted when:

- fixed fixtures produce the same packed cells and terminal dimensions;
- `Detailed240x67`, transformed `Detailed177x50`, YCbCr reduction, high-entropy
  live splicing, and CellTape export have recorded before/after results;
- a representative 30 FPS live-preview and recording run stays near cadence
  with bounded heap and queue depth;
- normal CellTape export performs one validation pass and one borrowed-frame
  emission pass;
- cancellation, malformed strides, hostile timestamps, output collisions, and
  invalid FFmpeg candidates have deterministic tests;
- `deadcode -test ./...` reports no unreachable repository functions;
- `govulncheck` reports no reachable known vulnerability;
- `go test ./...`, `go vet ./...`, and the trimmed production build pass.

## Measurements

Measured on Windows/AMD64 with a Ryzen 5 5600X and Go 1.26.6. `Before` is the
merge base `5505639`; `Phase 1` is the current implementation. Times are
medians of five benchmark runs.

| Workload | Before | Phase 1 | Allocation |
|---|---:|---:|---:|
| Detailed solver, 240×67 cells | 8.95 ms | 4.30 ms | 0 B/op |
| Detailed solver plus Color Look, 177×50 cells | 5.02 ms | 2.37 ms | 0 B/op |
| YCbCr reduction, 480×270 to 240×67 cells | 1.50 ms | 1.08 ms | 0 B/op |
| High-entropy dashboard splice, 240×67 cells | 3.80 ms | 0.067 ms | 1 final page allocation |
| CellTape replay, 300×84 cells | 0.219 ms / 967 KB | 0.182 ms / 664 KB | 8 to 6 allocs/op |

Five final 1,800-frame soak runs ranged from -5,968 to +5,208 retained bytes
and -9 to +7 live objects after forced collection. The real C922 capture
delivered 29.85 FPS over the six-second cadence gate. The final
combined preview and CellTape run held 29.8 FPS while recording, retained
1,484,832 additional heap bytes after forced collections, and reached a queue
high-water mark of 1/120; MP4, GIF, and PNG output then completed.

At a one-frame queue high-water mark, the CellTape pool retains about 0.29 MiB
of cell backing at 300×84 instead of eventually warming all 240 slots to about
69.2 MiB. Its hard queue ceiling is unchanged.

Mosaic remains for the deterministic no-camera demo. At 240×67 it costs about
19.62 ms and 9.12 MB per generated demo frame, but it is not called by the live
camera path. Named Mosaic and `x/image` symbols account for about 76 KB of the
8.0 MB executable. Replacing it without changing the demo would add code while
removing little linked weight, so Phase 1 keeps the dependency.

## Not in Phase 1

New controls, visual redesign, new filters, cross-platform capture, packaging,
installer redesign, and distribution changes remain later roadmap work.
