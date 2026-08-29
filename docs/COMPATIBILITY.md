# Compatibility

The downloadable `v0.2.0-beta.1` release is for Windows x64 only. The cited source baseline also contains native Linux and macOS camera work, but those paths are not part of a published release and have not been accepted on real hardware yet. This page classifies published artifacts and recorded evidence; uncommitted source changes are not compatibility evidence.

This page uses precise status words:

- **Released**: a downloadable Inlaid build exists for the target.
- **Native CI**: tests, vet, vulnerability scan, and production build passed on the target operating system. It does not prove a camera or terminal.
- **Hardware verified**: a real camera, terminal, live preview, outputs, and camera lifecycle were exercised together and recorded.
- **Expected—unverified**: the design should work, but the project does not have enough evidence yet.
- **Known incompatible**: a documented requirement is absent or the current implementation cannot use the environment.
- **Observed regression**: a previously working path has a reproduced failure that is not accepted as fixed yet.
- **Characterization only**: the environment is exercised to record behavior and useful failure, but is not a modern-support promise.

Implementation and native CI results are evidence only for the exact revision and runner they record, not support levels by themselves.

## Platform status

| Target | Distribution | Source and CI evidence | Physical evidence | Classification |
|---|---|---|---|---|
| Windows 10 x64 | `v0.2.0-beta.1` | Media Foundation with native MJPEG at the cited `59aad21` candidate; no exact CI run is linked in this ledger | Logitech C922 acceptance passed on Windows 10 Home 22H2 in Windows Terminal on 2026-08-24 | **Released** and **Hardware verified** |
| Windows 11 x64 | `v0.2.0-beta.1` | The same Windows x64 artifact is downloadable; no Windows 11-specific CI or source-acceptance record is linked | None | **Released** and **Expected—unverified** |
| Linux x86_64 | None; the accepted [Linux distribution contract](DISTRIBUTION-LINUX.md) is policy, not a package | Experimental V4L2 MMAP capture, native MJPEG, and libturbojpeg decode at commit [`adb0942`](https://github.com/Melty1000/inlaid/commit/adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3); native CI run [`32782751639`](https://github.com/Melty1000/inlaid/actions/runs/32782751639) passed | None | **Expected—unverified** |
| macOS 15 Apple silicon | None; the accepted [macOS distribution contract](DISTRIBUTION-MACOS.md) is policy, not a package | Experimental AVFoundation capture with reduced NV12 frames at commit [`adb0942`](https://github.com/Melty1000/inlaid/commit/adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3); native arm64 CI run [`32782751639`](https://github.com/Melty1000/inlaid/actions/runs/32782751639) passed | None | **Expected—unverified** |

The Linux and macOS rows remain experimental until someone records the camera,
terminal, cadence, memory, and shutdown results from physical machines. Their
recorded native-CI claim applies only to the exact commit and run linked above;
it means each bridge compiled and its automated checks passed on that runner,
not that a camera or terminal worked. Uncommitted portability and distribution
seams are provisional and are not accepted evidence. A cross-compile with cgo
disabled does not compile either native camera bridge. Accepting a platform
distribution contract changes no row in this table by itself. Physical evidence
is required for a positive hardware-verified or public-binary claim, but bounded
**Expected—unverified** and **Known incompatible** source classifications do not
depend on physical hardware.

## Evidence ledger

| Evidence | Build and date | OS and architecture | Camera and mode | Terminal, shell, and launch | Result |
|---|---|---|---|---|---|
| Maintainer baseline | [`v0.1.0-beta.1`](https://github.com/Melty1000/inlaid/releases/tag/v0.1.0-beta.1), 2026-08-23 | Windows x64; exact OS build not recorded | Logitech C922, native MJPEG, approximately 30 FPS | Windows Terminal, PowerShell 7, `START-INLAID.cmd` | Live preview and saved-output baseline passed for that release; later revisions require separate evidence |
| Maintainer portability acceptance | [`59aad21`](https://github.com/Melty1000/inlaid/commit/59aad21414af47cf9cf98dd55125505df788edb4), `v0.2.0-beta.1` candidate, 2026-08-24 | Windows 10 Home 22H2, build 19045.7184, x64 | Logitech C922, 1920×1080 native MJPEG, 29.93 FPS timestamp cadence | Windows Terminal 1.24; direct executable guard and headless acceptance runner | [Compatibility report #6](https://github.com/Melty1000/inlaid/issues/6): 30.03 FPS camera delivery; ten-minute recording held 29.5 shown FPS with 1,553,200 bytes retained heap and queue high-water 1/120; PNG, MP4, GIF, close, and reopen passed |

New physical-machine evidence belongs in a [compatibility report](https://github.com/Melty1000/inlaid/issues/new?template=02-compatibility-report.yml). Each report records the version or commit, OS and architecture, general hardware, camera and actual mode, terminal, shell and launch method, duration and FPS, memory behavior, output results, and camera close/reopen behavior. The table changes only when linked evidence supports the change.

## Known and unverified environments

| Environment | Status | Reason or next evidence |
|---|---|---|
| Windows legacy Console Host | **Characterization only** | An unlinked maintainer observation from earlier development reported missing quadrant glyphs and markedly worse presentation; its exact Windows build, Console Host version, and Inlaid revision were not recorded, so it is a characterization hypothesis rather than compatibility evidence. Terminal-first Inlaid does not redirect or fall back from this host. |
| Windows on Arm | **Expected—unverified** | No release, native CI job, or physical-camera report. |
| Intel macOS source build | **Expected—unverified** | No native Intel CI job or physical-camera report; this source-only classification creates no binary-package claim. |
| First macOS `.pkg` on Intel | **Known incompatible** | The arm64-only first-package contract intentionally rejects Intel before changing payload; Intel package support would require a separately accepted architecture and evidence boundary. |
| Modern 24-bit-color Windows terminals other than Windows Terminal | **Expected—unverified** | Complete the finite pre-publication matrix in [TESTING.md](TESTING.md); do not generalize from one host. |
| Terminal without 24-bit ANSI color | **Known incompatible** | Full-color cell output requires separate foreground and background colors. |
| Linux camera with raw formats only | **Known incompatible** | The cited V4L2 portability baseline requires a native MJPEG mode. |
| Linux camera available only through PipeWire, a sandbox portal, or libcamera | **Known incompatible** | Those capture paths are not implemented. |

## Camera requirements

### Windows

The published beta and cited Windows portability baseline require a camera that exposes a native MJPEG (`MJPG`) mode through Media Foundation. Inlaid opens the exact device selected in the dashboard and verifies the negotiated mode before accepting frames. It does not silently switch cameras or convert NV12 or YUY2 input.

The Logitech C922 is the only model with full hardware evidence so far. Cameras without native MJPEG, some virtual cameras, and cameras already held by another application may fail to open.

### Linux

The cited Linux portability baseline talks directly to V4L2. The accepted
[Linux distribution contract](DISTRIBUTION-LINUX.md) keeps that boundary and
adds no support claim. The current capture path requires:

- a camera exposed as a local `/dev/video*` device;
- permission to open that device;
- a native MJPEG capture mode;
- a cgo-enabled build, a C compiler, `pkg-config`, and libturbojpeg 2.0 or newer development files.

That baseline prefers stable `/dev/v4l/by-id` and `/dev/v4l/by-path` identities, then a stable sysfs device identity. It never persists a replaceable `/dev/videoN` number, so a device with no stable identity is not shown. It verifies the format and frame cadence returned by the driver and bounds both each MMAP buffer and their combined mapped bytes. It does not support raw NV12 or YUYV cameras, PipeWire camera portals, libcamera-only devices, or cameras visible only through a sandbox portal.

### macOS

The cited macOS portability baseline uses AVFoundation and the camera's exact
AVFoundation unique ID. It requires a cgo-enabled build with Apple Clang and the
macOS SDK. The accepted [macOS contract](DISTRIBUTION-MACOS.md) deliberately
limits its first planned binary to Apple-silicon `arm64` with an explicit macOS
15.0 deployment target. That policy is not an artifact or support result;
Intel, universal, Rosetta-only, pre-15, and untested later-major claims remain
unclaimed.

The installed candidate would be a hardened, Developer ID-signed command with
an embedded camera purpose string and camera entitlement. macOS asks the user
for camera access; if access is denied or restricted, Inlaid cannot enumerate
or open a camera. Package installation never grants or requests access. A
physical report must record whether macOS attributes the command-line request to
Inlaid, the invoking terminal, or another displayed identity instead of
assuming the permission owner from native CI.

That baseline asks AVFoundation for reduced NV12 frames and preserves their range and color-matrix metadata. Built-in and external camera behavior still needs real-machine verification. Signed and hardened macOS distribution remains a later packaging outcome.

## Terminal and shell

Inlaid needs an interactive terminal with 24-bit ANSI foreground and background colors. The dashboard needs at least 80 columns by 24 rows. The terminal grid controls visible detail, so a larger window or a smaller terminal font creates more image cells.

Windows Terminal is the only terminal with full visual and camera evidence today. It is the evidence baseline, not a runtime prerequisite. Other modern truecolor terminals may work, but they are not verified by the project yet. Keyboard controls remain available when a terminal does not pass mouse events.

The shell is only responsible for discovering and starting the executable. Once Inlaid is running, camera capture, drawing, and recording do not depend on PowerShell, Command Prompt, Bash, or another shell. Installed use in native Windows shells is terminal-first: close existing instances of the chosen host, open a fresh host process and shell, and run `inlaid` through the user `PATH`. Inlaid does not look up, select, launch, or hand off to Windows Terminal or any other host. Explorer double-click and Start-menu activation are not supported entry points.

Linux invocation evidence is route-specific. A cgo-enabled executable built from
the pinned source baseline and run as `./inlaid` or by an explicitly recorded
source path is source evidence only. `/usr/bin/inlaid` and bare `inlaid` resolving
to that path are planned installed-package evidence and do not exist yet.
`inlaid.exe` under WSL is Windows-executable interoperability, not native Linux
evidence. Shell aliases, functions, hashes, user-local executables, and unrelated
distro packages that shadow `/usr/bin/inlaid` are collision evidence, never a
passing installed-command result.

macOS invocation evidence is also route-specific. The existing source build and
opt-in AVFoundation test are source evidence only. The candidate package's
planned installed result requires a fresh native shell where bare `inlaid`
resolves to `/usr/local/bin/inlaid`; the package does not edit `PATH` or shell
startup files. A copied binary, a Homebrew or MacPorts prefix, an alias, a
function, or a user-local shadow is not installed-package evidence.

The finite pre-publication matrix is a coverage matrix, not a Cartesian product:

| Host | Current classification | Validation coverage |
|---|---|---|
| Windows Terminal | **Hardware verified** for the recorded Windows 10/C922 baseline only | PowerShell 7 full host baseline plus representative Windows PowerShell 5.1, cmd, Git Bash, and Nushell delta rows |
| VS Code integrated terminal | **Expected—unverified** | PowerShell 7 full host baseline plus representative Windows PowerShell 5.1, cmd, Git Bash, and Nushell delta rows |
| WezTerm | **Expected—unverified** | PowerShell 7 baseline |
| Alacritty | **Expected—unverified** | PowerShell 7 baseline |
| Tabby | **Expected—unverified** | PowerShell 7 baseline |
| Warp | **Expected—unverified** | Availability-gated PowerShell 7 baseline; an unavailable or declined account/paid gate is recorded and remains unclaimed unless Cody makes it mandatory |
| Hyper | **Expected—unverified** | PowerShell 7 baseline |
| Cmder using its bundled ConEmu host | **Expected—unverified** | PowerShell 7 baseline for this exact product configuration |
| Standalone ConEmu | **Expected—unverified** | Separate PowerShell 7 baseline; Cmder evidence does not cover it |
| Git Bash's mintty | **Expected—unverified** | Native Git Bash pairing |
| Windows Console Host | **Characterization only** | Windows PowerShell 5.1 and cmd characterization; never a modern-support promise |

WSL Bash is separate opt-in Windows-executable interoperability characterization, not a primary installed-command row. It runs only when WSL is already enabled or Cody separately authorizes enabling it, invokes `inlaid.exe` under the [distribution contract](DISTRIBUTION.md#terminal-first-invocation-contract), and never proves bare `inlaid`. If authorization is declined or WSL is unavailable, the row is recorded and remains unclaimed without blocking unrelated publication unless Cody explicitly makes it mandatory. The exact pair list, evidence tiers, and completion rules live in [TESTING.md](TESTING.md). Defects are fixed and retested or explicitly classified before claims change.

## Saved media and FFmpeg

Live preview and PNG snapshots do not need FFmpeg. MP4 and GIF export does.

Installed Inlaid accepts a working FFmpeg from `INLAID_FFMPEG` or the system `PATH`; it does not write `.tools` under the installed program directory. The legacy published `v0.2.0-beta.1` launcher may download one pinned, checksum-verified Windows build for that release's portable route. The terminal-first portable ZIP has no launcher or downloader: it accepts `INLAID_FFMPEG`, `ffmpeg.exe` at its colocated `.tools\ffmpeg\bin` path, or the system `PATH`. Source layouts may use their colocated `.tools/ffmpeg` helper. Linux and macOS source users must currently install FFmpeg themselves or point `INLAID_FFMPEG` at it.

FFmpeg is probed before export and runs outside the live camera path. A missing or broken FFmpeg should disable MP4 and GIF export without blocking the camera or PNG snapshots.

## Installation status

There is no Linux package, macOS package or app bundle, Homebrew formula, or
universal installer yet. The Windows ZIP, `.cmd` launcher, and PowerShell setup
remain the only published distribution. Provisional Windows MSI and
portable-layout source work is not a published artifact or compatibility claim.
The accepted [Linux distribution contract](DISTRIBUTION-LINUX.md) selects a
future Ubuntu 24.04 LTS `amd64` `.deb`; the accepted
[macOS distribution contract](DISTRIBUTION-MACOS.md) selects a future Apple-silicon
macOS 15 signed, notarized, and stapled `.pkg`. Neither policy status is an
artifact, implementation result, physical acceptance, or compatibility claim.
Linux and macOS retain separate platform seams; native source code alone is not
a promise of release support.
