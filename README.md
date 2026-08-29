# Inlaid

Inlaid shows a live webcam as full-color block cells in a terminal. It can save the same cell image as a PNG, MP4, or GIF.

## Status

| Platform | Current status |
|---|---|
| Windows 10/11 x64 | A Windows beta is published. A Logitech C922 in Windows Terminal is the only hardware-verified setup so far. |
| Linux | Native source path is experimental. No packaged release or physical-camera verification yet. |
| macOS | Native source path is experimental. No packaged release or physical-camera verification yet. |

[Compatibility](docs/COMPATIBILITY.md) keeps build claims separate from real hardware evidence. See the [roadmap](docs/ROADMAP.md), [testing guide](docs/TESTING.md), [changelog](CHANGELOG.md), and [security policy](SECURITY.md), or [report a problem or hardware result](https://github.com/Melty1000/inlaid/issues/new/choose).

## Windows install and updates

The currently published `v0.2.0-beta.1` ZIP is the legacy distribution described by that release's notes. The next Windows distribution is terminal-first and is not published until its MSI, terminal, signing, and release gates are accepted.

The terminal-first artifact is a per-user x64 MSI. It installs to `%LOCALAPPDATA%\Programs\Inlaid`, registers uninstall, and adds that directory to the current user's `PATH` only when no equivalent user-owned segment exists. It creates no Start-menu or desktop shortcut and needs no administrator rights. After install, repair, upgrade, or uninstall, close every process of the terminal host you are testing, open a fresh host and native Windows shell, then run `inlaid`. A new tab in an old host may still have the inherited pre-install environment.

Inlaid uses the terminal and working directory from which it was invoked. It never selects or launches Windows Terminal, another terminal host, or a shell. Explorer double-click and Start-menu activation are not supported entry points. A different `inlaid` earlier on `PATH` remains user-owned; use `where.exe inlaid` and the shell's command lookup to identify and resolve that collision.

Run a newer MSI to upgrade or the same MSI to repair. Uninstall removes program files, Windows Installer registration, every known installer-private PATH-provenance value, and only the exact PATH segment that provenance proves the MSI added. Empty `Installer` or `Components` registry keys may remain because deleting a key after checking that it is empty would race with foreign content. Settings, recovery tapes, recordings, snapshots, filters, support reports, foreign registry content, and ambiguous or pre-existing PATH text are retained.

WinGet is the intended discovery and update channel once its separately authorized public manifest is available. It will download that same immutable release MSI rather than a second installer build. Inlaid has no in-app updater.

The portable ZIP is for removable, isolated, and package-manager-free use:

1. Download and verify the ZIP checksum from its release.
2. Extract the whole archive.
3. Open a terminal in the extracted directory and run `.\inlaid.exe`.

Portable updates are manual and manifest-scoped. Keep the existing portable folder, close Inlaid, download and verify the newer ZIP, then extract that ZIP into a separate temporary directory and run the new package's root-level update helper:

```powershell
$package = 'C:\Downloads\inlaid-vNEXT-windows-amd64.zip'
$staging = Join-Path ([System.IO.Path]::GetTempPath()) ('inlaid-update-' + [Guid]::NewGuid().ToString('N'))
Expand-Archive -LiteralPath $package -DestinationPath $staging
& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $staging 'inlaid-vNEXT-windows-amd64\update-portable.ps1') -Package $package -PortableRoot 'D:\Portable Apps\Inlaid'
```

The helper is included at the portable ZIP root and is compatible with the Windows PowerShell 5.1 already present on supported Windows systems. The command applies `ExecutionPolicy Bypass` only to that verified package-helper process; it does not change the user's or machine's policy. Running the new copy keeps the updater forward-compatible even when the old portable folder lacks a newer helper. It stages only release-owned files, uses the prior portable manifest to prove any obsolete file it removes, and keeps a rollback snapshot until the new manifest commits last. Re-running it validates and recovers an interrupted update before retrying. It never replaces the whole portable root. It preserves the portable marker's authority and presence by atomically advancing `inlaid-portable.json` to the exact new versioned manifest; settings, recovery, recordings, snapshots, custom filters, support reports, and optional `.tools` remain byte-identical. The ZIP does not contain the source-only `START-INLAID.cmd`, `START-INLAID.ps1`, or `scripts\install-ffmpeg.ps1`.

WSL is a separate opt-in Windows-executable interoperability route. When WSL interoperability and Windows PATH import are already enabled, invoke `inlaid.exe`; bare `inlaid` searches for a Linux program and is not Windows installed-command evidence.

MP4 and GIF files need FFmpeg. It is not bundled with Inlaid. Installed and portable builds discover FFmpeg through `INLAID_FFMPEG`, the portable `.tools\ffmpeg\bin\ffmpeg.exe` location, or `PATH`; they never download it. For a portable copy, install a trusted FFmpeg yourself and either set `INLAID_FFMPEG`, put it on `PATH`, or place `ffmpeg.exe` at `.tools\ffmpeg\bin\ffmpeg.exe` below the portable root. The source checkout includes the pinned helper:

```powershell
pwsh -File .\scripts\install-ffmpeg.ps1
```

FFmpeg is used for saved MP4 and GIF files only. It is not part of the live camera path.

## Controls

Every control and button on the dashboard can be clicked.

- **Camera** selects the input device.
- **View** switches between filling the preview and showing the whole camera image.
- **Mirror** flips the image horizontally.
- **Detail** changes the cell style: Soft, Balanced, or Crisp.
- **Color Look** selects None, Warm, Cool, Mono, or a custom `.cube` look. **Mix** controls its strength.
- **Save As**, **Size**, **FPS**, and **Quality** set up the next recording. **Size** also sets the PNG canvas.
- **Pause Preview** holds the current frame without closing the camera. **Record** starts or stops a recording, **Snapshot** writes a PNG, and **Open Folder** opens the output directory.
- **Report** creates a local troubleshooting report after a second confirmation. It never includes camera media, paths, device IDs, or an upload. After it is saved, **Open Folder** opens the report's location so you can review the JSON before deciding whether to attach it to a public issue.
- **Details** shows the camera mode, terminal grid, frame rate, and skipped frames.

Keyboard shortcuts:

| Key | Action |
|---|---|
| `Tab` / `Down` | Next control |
| `Shift+Tab` / `Up` | Previous control |
| `Left` / `Right` or `h` / `l` | Change the focused control |
| `Enter` / `Space` | Activate the focused control |
| `r` | Start or stop recording |
| `s` | Save a snapshot |
| `p` | Pause or resume the preview |
| `q`, `Esc`, or `Ctrl+C` | Quit; an active recording is saved first |

## Camera size, terminal size, and saved size

These are separate:

- **Camera mode** is what the webcam sends. Inlaid asks for 1920×1080 at 30 FPS by default.
- **Terminal grid** is the detail you can actually see, measured in character cells. `177×50` means 177 columns by 50 rows. The dashboard needs at least 80×24. In Windows Terminal, `Ctrl+-` makes the text smaller and creates more cells; `Ctrl++` makes it larger and creates fewer.
- **Saved size** is the 720p or 1080p canvas chosen under **Size**. The current terminal cells are enlarged to fit that canvas. A 1080p file does not add detail that was not visible in the terminal grid.

The live view aims for 30 FPS. There is no artificial 24 FPS cap or minimum. The result depends on the camera mode, terminal grid, terminal host, and the machine. A camera reporting 29.97 FPS is normal. If a saved file uses a higher FPS than the number of unique displayed states, states are repeated so the timing stays correct.

## Camera support

The published beta requires a camera that exposes a native MJPEG (`MJPG`) mode through Windows Media Foundation. The Logitech C922 is the only model that has been tested.

Inlaid uses the closest compatible MJPEG mode when the requested size and frame rate are unavailable. It does not switch to another camera or convert NV12/YUY2 input behind the scenes. Cameras without MJPEG, some virtual cameras, and devices already in use by another program may not open.

Current source builds have separate experimental V4L2 and AVFoundation backends. Their requirements and current evidence are listed in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

## Color Looks

None, Warm, Cool, and Mono are built in. For a custom look, put a trusted Adobe/IRIDAS or DaVinci Resolve `.cube` file directly in the active filters directory, then restart the app. Installed Windows builds use `Inlaid\Filters` below the Windows Documents known folder; `%USERPROFILE%\Documents\Inlaid\Filters` is the ordinary default, but Windows may redirect that known folder. Portable and source runs use `filters\` below their own root.

Looks are applied to the finished cell colors. The terminal, snapshot, and recording therefore use the same colors, but a look cannot add image detail. See [docs/FILTERS.md](docs/FILTERS.md) for the supported `.cube` syntax and limits.

## Saved files, recovery, and portable import

Documents, Videos, and Pictures below are Windows known folders. The `%USERPROFILE%` paths shown are ordinary defaults; Windows folder redirection can place them elsewhere.

| Data | Installed Windows location | Portable/source location |
|---|---|---|
| Settings and recovery | `%LOCALAPPDATA%\Inlaid\inlaid-settings.json`; `%LOCALAPPDATA%\Inlaid\Recovery` | beside the executable/root |
| Recordings | Windows Videos known folder under `Inlaid` (ordinary default `%USERPROFILE%\Videos\Inlaid`) | `recordings\` |
| Snapshots | Windows Pictures known folder under `Inlaid` (ordinary default `%USERPROFILE%\Pictures\Inlaid`) | `snapshots\` |
| Filters and support reports | Windows Documents known folder under `Inlaid\Filters` and `Inlaid\Support Reports` (ordinary defaults `%USERPROFILE%\Documents\Inlaid\Filters` and `%USERPROFILE%\Documents\Inlaid\Support Reports`) | `filters\`; `support-reports\` |

Recording has no preset time limit. While it runs, Inlaid writes the displayed cell states to a recoverable CellTape on disk. MP4 or GIF conversion begins after you press Stop, so a long recording can take time to finish. Disk use continues until you stop recording or the drive reports an error.

If the app closes or conversion fails, it keeps the CellTape and tries the export again next time it starts. Automatic recovery skips a tape whose timestamps would produce more than seven days of video; the tape is left untouched. Keep `recordings\.recovery\` if you want that recovery.

Saved media uses the same crop, mirror, detail, terminal grid, and Color Look as the live view. PNG keeps the cell raster directly. MP4 uses lossy H.264, and GIF is limited to a color palette. No audio is recorded.

To import settings and custom filters from a portable folder into an installed copy, choose that folder explicitly:

```powershell
inlaid --import-portable 'D:\Portable Apps\Inlaid'
```

Import accepts current portable installs that carry their direct regular `inlaid-portable.json` marker. The only markerless exception is the exactly pinned release-owned file and directory shape of the published `v0.2.0-beta.1` ZIP; arbitrary, ambiguous, source-tree, and current-layout markerless folders are refused before anything is copied. Accepted imports copy without deleting or overwriting, report copied, skipped, and conflicting items, leave recordings/snapshots/reports where they are, and refuse a folder containing live recovery tapes. Finish or abandon recovery in the portable copy first.

The app has no telemetry or upload feature. A support report is written only after two deliberate button presses, and it excludes camera media and machine-specific identifiers. Review it before manually attaching it to a public issue.

If this folder already contains `webcam-settings.json` from a pre-Inlaid build, Inlaid uses it as the starting settings and saves later changes to `inlaid-settings.json`. The old file is not copied, overwritten, or deleted.

## Build from source

For a Windows source checkout, install PowerShell 7 and Go 1.26 or newer, open the truecolor terminal you want Inlaid to use, then run:

```powershell
git clone https://github.com/Melty1000/inlaid.git
Set-Location .\inlaid
.\scripts\setup.ps1 -Check
.\START-INLAID.cmd
```

`START-INLAID.cmd` invokes the PowerShell source launcher in that same terminal. It passes the checkout as the explicit source data root and never opens or hands off to another terminal. `setup.ps1` downloads the Go modules, checks or installs FFmpeg, runs the tests and `go vet`, and builds `bin\inlaid.exe`. It uses Go from `PATH` or `.tools\go\bin\go.exe`; it does not download Go.

Linux source builds additionally need a C toolchain, `pkg-config`, and libturbojpeg 2.0 or newer development files. macOS source builds need Apple Clang and the macOS SDK. Both are experimental and currently use normal Go build commands rather than a finished installer or launcher. See [Compatibility](docs/COMPATIBILITY.md) before treating either one as supported.

See [CONTRIBUTING.md](CONTRIBUTING.md) before opening a change. The rendering path is documented in [docs/CELL_PIPELINE.md](docs/CELL_PIPELINE.md).

## License

[MIT](LICENSE). Dependency and FFmpeg notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
