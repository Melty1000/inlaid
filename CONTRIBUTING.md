# Contributing

Inlaid is a Windows application. Changes to capture, layout, recording, or launch behavior should be verified in Windows Terminal, not only through headless tests.

## Development setup

Install:

- Windows 10 or 11
- PowerShell 7
- Windows Terminal
- Go 1.26 or newer

From the repository root:

```powershell
.\scripts\setup.ps1 -Check
.\START-INLAID.cmd
```

Setup uses installed Go, or `.tools\go\bin\go.exe` when already present. It checks or acquires FFmpeg, downloads Go modules, runs the test/vet checks requested by `-Check`, and builds `bin\inlaid.exe` from `cmd/inlaid`.

For the normal local gate:

```powershell
go test ./...
go vet ./...
go build -trimpath -o .\bin\inlaid.exe .\cmd\inlaid
```

## Hardware tests

Ordinary tests do not turn on a camera. Real-camera checks are opt-in:

```powershell
$env:INLAID_MF_CAPTURE_REAL = '1'
go test .\internal\mfcapture .\internal\cellreduce

$env:INLAID_LIVE_TEST = '1'
$env:INLAID_TEST_DEVICE = 'Your Media Foundation camera name'
go test .\internal\dashboard
```

The low-light camera-control checks and some dashboard acceptance cases are currently specific to the Logitech C922. The three-minute capture soak additionally requires `INLAID_MF_CAPTURE_SOAK=1` and should not be part of routine runs.

Unset opt-in variables after testing so later test runs do not unexpectedly open the camera.

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

Describe the user-visible result, the camera/terminal environment used, and the checks you ran. Include before/after captures for visual changes, but remove private camera content and machine-specific paths first.

Do not commit `recordings\`, `snapshots\`, `.tools\`, local settings, generated binaries, or recovery tapes.

Security reports belong in the private process described in [SECURITY.md](SECURITY.md), not in a public issue or pull request.
