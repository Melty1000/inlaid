# Inlaid

Inlaid shows a live webcam as full-color block cells in Windows Terminal. It can save the same cell image as a PNG, MP4, or GIF.

This beta is an x64 build for Windows 10 and 11; Windows on Arm has not been tested. The only camera tested so far is a Logitech C922.

## Download and run

You need Windows Terminal and PowerShell 7.

1. Download the Windows ZIP from [Releases](https://github.com/Melty1000/inlaid/releases).
2. Extract the whole ZIP.
3. Double-click `START-INLAID.cmd`.

Use the `.cmd` file for double-clicking; Windows often opens `.ps1` files in an editor. The executable is not signed yet, so Windows SmartScreen may show a warning on first launch. `SHA256SUMS.txt` on the release page contains the ZIP checksum.

MP4 and GIF files need FFmpeg. It is not bundled with Inlaid. If no working FFmpeg is found, the launcher makes one attempt to download a pinned, checksum-verified copy into `.tools\ffmpeg`. A failed download does not block the live view or PNG snapshots; run the launcher again while online to retry. You can also run:

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

The live view aims for 30 FPS. There is no artificial 24 FPS cap or minimum. The result depends on the camera mode, terminal grid, Windows Terminal, and the machine. A camera reporting 29.97 FPS is normal. If a saved file uses a higher FPS than the number of unique displayed states, states are repeated so the timing stays correct.

## Camera support

The camera must expose a native MJPEG (`MJPG`) mode through Windows Media Foundation. The Logitech C922 is the only model that has been tested.

Inlaid uses the closest compatible MJPEG mode when the requested size and frame rate are unavailable. It does not switch to another camera or convert NV12/YUY2 input behind the scenes. Cameras without MJPEG, some virtual cameras, and devices already in use by another program may not open.

## Color Looks

None, Warm, Cool, and Mono are built in. For a custom look, put a trusted Adobe/IRIDAS or DaVinci Resolve `.cube` file directly in `filters\`, then restart the app.

Looks are applied to the finished cell colors. The terminal, snapshot, and recording therefore use the same colors, but a look cannot add image detail. See [docs/FILTERS.md](docs/FILTERS.md) for the supported `.cube` syntax and limits.

## Saved files and recovery

- PNG snapshots: `snapshots\`
- MP4 and GIF recordings: `recordings\`
- Settings: `inlaid-settings.json`
- Recording recovery data: `recordings\.recovery\`

Recording has no preset time limit. While it runs, Inlaid writes the displayed cell states to a recoverable CellTape on disk. MP4 or GIF conversion begins after you press Stop, so a long recording can take time to finish. Disk use continues until you stop recording or the drive reports an error.

If the app closes or conversion fails, it keeps the CellTape and tries the export again next time it starts. Keep `recordings\.recovery\` if you want that recovery.

Saved media uses the same crop, mirror, detail, terminal grid, and Color Look as the live view. PNG keeps the cell raster directly. MP4 uses lossy H.264, and GIF is limited to a color palette. No audio is recorded.

Camera frames, settings, recovery data, and saved files stay in the extracted folder. The app has no telemetry or upload feature. Network access is used only to fetch FFmpeg when needed and, in a source checkout, Go modules.

If this folder already contains `webcam-settings.json` from a pre-Inlaid build, Inlaid uses it as the starting settings and saves later changes to `inlaid-settings.json`. The old file is not copied, overwritten, or deleted.

## Build from source

Install Windows Terminal, PowerShell 7, and Go 1.26 or newer, then run:

```powershell
git clone https://github.com/Melty1000/inlaid.git
Set-Location .\inlaid
.\scripts\setup.ps1 -Check
.\START-INLAID.cmd
```

`setup.ps1` downloads the Go modules, checks or installs FFmpeg, runs the tests and `go vet`, and builds `bin\inlaid.exe`. It uses Go from `PATH` or `.tools\go\bin\go.exe`; it does not download Go.

See [CONTRIBUTING.md](CONTRIBUTING.md) before opening a change. The rendering path is documented in [docs/CELL_PIPELINE.md](docs/CELL_PIPELINE.md).

## License

[MIT](LICENSE). Dependency and FFmpeg notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
