# Contributing

The published Inlaid beta is a Windows application. The current source also has experimental Linux and macOS camera backends. Changes to capture, layout, recording, or launch behavior need native terminal and camera evidence; headless tests alone are not acceptance.

The current evidence and platform requirements are kept in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md). The exact checks and hardware report format are in [docs/TESTING.md](docs/TESTING.md).

## Development setup

All platforms need Go 1.26 or newer and a terminal with 24-bit ANSI color.

Windows development additionally uses:

- Windows 10 or 11
- PowerShell 7
- Windows Terminal

From the repository root:

```powershell
.\scripts\setup.ps1 -Check
.\START-INLAID.cmd
```

Setup uses installed Go, or `.tools\go\bin\go.exe` when already present. It checks or acquires FFmpeg, downloads Go modules, runs the test/vet checks requested by `-Check`, and builds `bin\inlaid.exe` from `cmd/inlaid`.

Linux native camera builds need a C compiler, `pkg-config`, and libturbojpeg 2.0 or newer development files. On Ubuntu, the package names are `pkg-config` and `libturbojpeg0-dev`. macOS native camera builds need Apple Clang and the macOS SDK, normally supplied by Xcode Command Line Tools.

The normal source gate is the same on every platform:

```text
go test ./...
go vet ./...
go build -trimpath ./cmd/inlaid
```

The Linux and macOS jobs in CI are configured for these native dependencies, but the unpublished platform work is not accepted until those jobs pass and real-camera checks are recorded.

## Hardware tests

Ordinary tests do not open a camera. Real-camera checks are deliberately opt-in and must be run on the operating system being claimed. Follow [docs/TESTING.md](docs/TESTING.md), then submit the result with the compatibility-report issue form. A cross-compile or passing CI job is not camera verification.

## Change guidelines

- Keep camera packets, decoded frames, canonical cells, and recovery data bounded. Do not add an unbounded live queue.
- Preserve latest-wins preview delivery. A slow terminal should drop stale states, not accumulate latency.
- Keep one canonical `CellFrame` for preview, snapshot, CellTape, and export. An export-only detail path would violate the product behavior.
- Keep full-canvas rasterization and FFmpeg encoding off the live camera/UI loop.
- Pass executable arguments structurally; do not assemble user-controlled shell command strings.
- Treat `.cube` files, settings, recovery tapes, terminal resize events, and camera metadata as untrusted input.
- Keep the C922-specific exposure/gain behavior gated to that camera and restore every changed device control.
- Preserve unknown settings fields when changing the settings schema.
- Update README, CHANGELOG, pipeline/filter docs, and third-party notices when behavior or dependencies change.

## Pull requests

Describe the user-visible result, the camera and terminal environment used, and the checks you ran. The pull-request template lists the evidence expected for each kind of change. Include before/after captures for visual changes only when they are useful, and remove private camera content and machine-specific paths first.

Do not commit `recordings\`, `snapshots\`, `.tools\`, local settings, generated binaries, or recovery tapes.

Security reports belong in the private process described in [SECURITY.md](SECURITY.md), not in a public issue or pull request.
