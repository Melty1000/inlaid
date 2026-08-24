# Compatibility

The downloadable `v0.2.0-beta.1` release is for Windows x64 only. The source tree also contains native Linux and macOS camera work, but those paths are not part of a published release and have not been accepted on real hardware yet.

This page uses precise status words:

- **Released**: a downloadable Inlaid build exists for the target.
- **Native CI**: tests, vet, vulnerability scan, and production build passed on the target operating system. It does not prove a camera or terminal.
- **Hardware verified**: a real camera, terminal, live preview, outputs, and camera lifecycle were exercised together and recorded.
- **Expected—unverified**: the design should work, but the project does not have enough evidence yet.
- **Known incompatible**: a documented requirement is absent or the current implementation cannot use the environment.
- **Observed regression**: a previously working path has a reproduced failure that is not accepted as fixed yet.

Implementation and configured CI are facts about the source tree, not support levels by themselves.

## Platform status

| Target | Distribution | Current source | Physical evidence | Classification |
|---|---|---|---|---|
| Windows 10/11 x64 | `v0.2.0-beta.1` | Media Foundation with native MJPEG; Windows CI configured | Logitech C922 release-candidate acceptance passed in Windows Terminal at approximately 30 FPS on 2026-08-24 | **Released** and **Hardware verified** |
| Linux x86_64 | None | Experimental V4L2 MMAP capture, native MJPEG, and libturbojpeg decode; native CI configured | None | **Expected—unverified** |
| macOS Apple silicon | None | Experimental AVFoundation capture with reduced NV12 frames; native CI configured | None | **Expected—unverified** |

The Linux and macOS rows should remain marked experimental until their native jobs pass and someone records the camera, terminal, cadence, memory, and shutdown results from physical machines. A cross-compile with cgo disabled does not compile either native camera bridge.

## Evidence ledger

| Evidence | Build and date | OS and architecture | Camera and mode | Terminal, shell, and launch | Result |
|---|---|---|---|---|---|
| Maintainer baseline | [`v0.1.0-beta.1`](https://github.com/Melty1000/inlaid/releases/tag/v0.1.0-beta.1), 2026-08-23 | Windows x64; exact OS build not recorded | Logitech C922, native MJPEG, approximately 30 FPS | Windows Terminal, PowerShell 7, `START-INLAID.cmd` | Live preview and saved-output baseline passed; current-source portability acceptance remains separate |
| Maintainer portability acceptance | [`v0.2.0-beta.1`](https://github.com/Melty1000/inlaid/releases/tag/v0.2.0-beta.1), 2026-08-24 | Windows 10 Home 22H2, build 19045.7184, x64 | Logitech C922, 1920×1080 native MJPEG, 29.93 FPS timestamp cadence | Windows Terminal 1.24; direct executable guard and headless acceptance runner | 30.02 FPS camera delivery; short recording smoke held 29.8 FPS with +1.4 MiB retained heap; PNG, MP4, GIF, close, and reopen passed |

New physical-machine evidence belongs in a [compatibility report](https://github.com/Melty1000/inlaid/issues/new?template=02-compatibility-report.yml). Each report records the version or commit, OS and architecture, general hardware, camera and actual mode, terminal, shell and launch method, duration and FPS, memory behavior, output results, and camera close/reopen behavior. The table changes only when linked evidence supports the change.

## Known and unverified environments

| Environment | Status | Reason or next evidence |
|---|---|---|
| Windows legacy Console Host | **Known incompatible** | It can lack the quadrant glyphs used by Crisp mode and showed markedly worse presentation. Current source redirects a plain Explorer double-click into Windows Terminal; manually starting Inlaid inside legacy Console Host remains unsupported. |
| Windows on Arm | **Expected—unverified** | No release, native CI job, or physical-camera report. |
| Intel macOS | **Expected—unverified** | No native CI job or physical-camera report. |
| Other modern 24-bit-color terminals | **Expected—unverified** | Submit a physical-terminal compatibility report. |
| Terminal without 24-bit ANSI color | **Known incompatible** | Full-color cell output requires separate foreground and background colors. |
| Linux camera with raw formats only | **Known incompatible** | The current V4L2 path requires a native MJPEG mode. |
| Linux camera available only through PipeWire, a sandbox portal, or libcamera | **Known incompatible** | Those capture paths are not implemented. |

## Camera requirements

### Windows

The published beta and current Windows source require a camera that exposes a native MJPEG (`MJPG`) mode through Media Foundation. Inlaid opens the exact device selected in the dashboard and verifies the negotiated mode before accepting frames. It does not silently switch cameras or convert NV12 or YUY2 input.

The Logitech C922 is the only model with full hardware evidence so far. Cameras without native MJPEG, some virtual cameras, and cameras already held by another application may fail to open.

### Linux

The current Linux source talks directly to V4L2. It requires:

- a camera exposed as a local `/dev/video*` device;
- permission to open that device;
- a native MJPEG capture mode;
- a cgo-enabled build, a C compiler, `pkg-config`, and libturbojpeg 2.0 or newer development files.

The backend prefers stable `/dev/v4l/by-id` and `/dev/v4l/by-path` identities, then a stable sysfs device identity. It never persists a replaceable `/dev/videoN` number, so a device with no stable identity is not shown. It verifies the format and frame cadence returned by the driver and bounds both each MMAP buffer and their combined mapped bytes. It does not currently support raw NV12 or YUYV cameras, PipeWire camera portals, libcamera-only devices, or cameras visible only through a sandbox portal.

### macOS

The current macOS source uses AVFoundation and the camera's exact AVFoundation unique ID. It requires a cgo-enabled build with Apple Clang and the macOS SDK. macOS will ask the user for camera access. If access is denied, Inlaid cannot enumerate or open a camera.

The backend asks AVFoundation for reduced NV12 frames and preserves their range and color-matrix metadata. Built-in and external camera behavior still needs real-machine verification. Signed and hardened macOS distribution also belongs to the later packaging phase.

## Terminal and shell

Inlaid needs an interactive terminal with 24-bit ANSI foreground and background colors. The dashboard needs at least 80 columns by 24 rows. The terminal grid controls visible detail, so a larger window or a smaller terminal font creates more image cells.

Windows Terminal is the only terminal with full visual and camera evidence today. Other modern truecolor terminals may work, but they are not verified by the project yet. Keyboard controls remain available when a terminal does not pass mouse events.

The shell is only responsible for starting the executable. Once Inlaid is running, camera capture, drawing, and recording do not depend on PowerShell, Command Prompt, Bash, Zsh, or another shell. The current double-click launcher and setup scripts are Windows conveniences, not a cross-platform installation system.

## Saved media and FFmpeg

Live preview and PNG snapshots do not need FFmpeg. MP4 and GIF export does.

Inlaid accepts a working FFmpeg from `INLAID_FFMPEG`, the system `PATH`, or the `.tools/ffmpeg` directory beside its installation. The published Windows launcher can download one pinned, checksum-verified Windows build. Linux and macOS source users must currently install FFmpeg themselves or point `INLAID_FFMPEG` at it.

FFmpeg is probed before export and runs outside the live camera path. A missing or broken FFmpeg should disable MP4 and GIF export without blocking the camera or PNG snapshots.

## Installation status

There is no Linux package, macOS app bundle, Homebrew formula, or universal installer yet. The Windows ZIP, `.cmd` launcher, and PowerShell setup remain the only published distribution. Reworking installation and packaging is a later phase; native source code alone is not a promise of release support.
