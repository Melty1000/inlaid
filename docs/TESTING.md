# Testing Inlaid

Tests fall into three groups. Keep the result you claim no stronger than the test you ran.

| Evidence | What it proves |
|---|---|
| Cross-build | The shared Go code compiles for a target. It does not compile or exercise a cgo camera bridge. |
| Native CI | Tests, vet, vulnerability scan, and production build pass on that operating system. It does not prove a physical camera or terminal. |
| Hardware report | A named camera, terminal, and build worked together on a physical machine for the recorded checks. |

## Source gate

All platforms require Go 1.26 or newer. Linux native builds also need a C compiler, `pkg-config`, and libturbojpeg 2.0 or newer development files. macOS native builds need Apple Clang and the macOS SDK.

Run from the repository root:

```text
go test ./...
go vet ./...
go build -trimpath ./cmd/inlaid
```

The release gate also runs `govulncheck` and the native jobs in `.github/workflows/ci.yml`. Do not publish a platform build from a cross-build alone.

## Physical-camera checks

Ordinary tests never open a camera. The following checks are opt-in.

### Windows

```powershell
$env:INLAID_MF_CAPTURE_REAL = '1'
go test .\internal\capture .\internal\cellreduce

$env:INLAID_LIVE_TEST = '1'
$env:INLAID_TEST_DEVICE = 'Camera name shown by Inlaid'
go test -v .\internal\dashboard -run 'TestRuntimeLiveCameraRecordAndSnapshot|TestBubbleTeaProgramReceivesLiveCameraPreview'
```

Some Media Foundation lighting checks are specific to the Logitech C922. The optional three-minute capture soak also requires `INLAID_MF_CAPTURE_SOAK=1`; it is not part of routine development. Setting `INLAID_LIVE_SOAK=1` with `INLAID_LIVE_TEST=1` extends the dashboard recording check to ten minutes, measures retained heap and queue pressure, and decodes the resulting MP4, GIF, and PNG.

Run the application through `START-INLAID.cmd` or start it from the terminal being tested. Record the launch method. Current source redirects a plain Explorer double-click of the raw Windows executable into Windows Terminal; manually running Inlaid inside legacy Console Host is not an accepted terminal test.

### Linux

```text
INLAID_V4L2_CAPTURE_REAL=1 go test -v ./internal/capture
INLAID_LIVE_TEST=1 INLAID_TEST_DEVICE='Camera name shown by Inlaid' go test -v ./internal/dashboard -run 'TestRuntimeLiveCameraRecordAndSnapshot|TestBubbleTeaProgramReceivesLiveCameraPreview'
```

`INLAID_V4L2_CAPTURE_DEVICE` may select an exact device ID or displayed name. If it is unset, the capture test uses the first enumerated camera. Do not publish a stable device path, USB serial, host name, or username in the report.

### macOS

```text
INLAID_AVF_CAPTURE_REAL=1 go test -v ./internal/capture
INLAID_LIVE_TEST=1 INLAID_TEST_DEVICE='Camera name shown by Inlaid' go test -v ./internal/dashboard -run 'TestRuntimeLiveCameraRecordAndSnapshot|TestBubbleTeaProgramReceivesLiveCameraPreview'
```

Grant camera access to the terminal or executable running the test. Record whether permission was first granted, already granted, or denied and recovered; do not publish the AVFoundation unique ID.

Unset the opt-in variables after testing so a later test run cannot open the camera unexpectedly.

## Acceptance run

Use a physical machine and keep the application open long enough to expose cadence or retained-memory problems. Ten minutes is a useful baseline; longer reports are welcome.

Record:

- release tag or exact commit and how it was obtained;
- OS or distribution version and architecture;
- general machine class without serial numbers or account names;
- camera model and the actual mode selected by Inlaid;
- terminal and version, shell and version, and launch method;
- terminal grid, sustained source and shown FPS, skipped frames, and test duration;
- whether retained memory stabilized or continued growing;
- PNG, MP4, and GIF results, including recording duration;
- camera close, reopen, removal, permission-denied, and another-application ownership behavior when exercised.

Submit the result with the [compatibility report form](https://github.com/Melty1000/inlaid/issues/new?template=02-compatibility-report.yml). A failure is useful evidence; describe it instead of marking the entire platform unsupported.

## Privacy

Issue text and attachments in this repository are public. Remove camera images, recordings, absolute paths, usernames, host names, device IDs, serial numbers, tokens, and private terminal output before submitting.

An Inlaid support report is created locally only when requested and is never uploaded by the app. Review it before attaching it. Security vulnerabilities and sensitive proof of concept belong in a [private security advisory](https://github.com/Melty1000/inlaid/security/advisories/new), not a compatibility issue.
