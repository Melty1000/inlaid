# Phase 2: Native portability

Status: implemented and build-tested in native Windows, Linux, and macOS CI.
Physical Linux and macOS camera acceptance remains open.

## Contract

Phase 2 makes the refined Inlaid core build and run natively on Windows, Linux,
and macOS without changing the picture, dashboard controls, recording formats,
or bounded-memory behavior established in Phase 1.

Camera identity, mode selection, capture, frame lifetime, shutdown, folder
opening, FFmpeg discovery, and recovery durability may differ by operating
system. The cell solver, dashboard, CellTape format, exports, settings, and
terminal projection do not.

The executable does not depend on the shell after launch. Installation,
packaging, visual redesign, and new product features are outside this phase.

## Platform paths

- Windows retains Media Foundation capture and reduced WIC MJPEG decode.
- Linux uses V4L2 streaming with stable device identities, MMAP buffers, and
  reduced planar decoding through the system libturbojpeg.
- macOS uses AVFoundation unique IDs and reduced NV12 frames with explicit
  range and color-matrix metadata.

All three paths feed the same owned `capture.Frame` contract. Live handoffs are
bounded and latest-wins. Closing a camera must stop new callbacks, release all
queued frames, and either confirm native teardown or return an uncertain-
shutdown error that prevents an unsafe reopen.

## Acceptance

Phase 2 is accepted only when:

- independent Standards and Spec reviews have no unresolved findings;
- `go test ./...` and `go vet ./...` pass on Windows, Linux, and macOS;
- native production builds succeed on all three platforms;
- representative cameras on all three platforms sustain approximately 30 FPS
  through live preview and recording within documented memory bounds;
- close, reopen, camera removal, permission failure, and another-application
  ownership have platform evidence; and
- the compatibility matrix distinguishes published, implemented, build-tested,
  and hardware-verified support.

Current evidence is recorded in [COMPATIBILITY.md](COMPATIBILITY.md). A
cgo-disabled cross-build proves the shared Go core, not either Unix camera
bridge, and cannot close this phase by itself.

## Not in Phase 2

Installers, package managers, signed application bundles, a redesigned
dashboard, split-view terminal orchestration, new filters, audio, and new
recording formats remain later work.
