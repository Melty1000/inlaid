# Cell pipeline

Inlaid carries only the information a terminal can display. A terminal cell has a block mask and two RGB colors, so that compact cell state becomes the canonical frame shared by the preview and saved media.

```text
selected camera
    │ native timestamped MJPEG
    ▼
Windows Media Foundation
    │ bounded packet handoff
    ▼
WIC reduced JPEG decode
    │ planar Y/Cb/Cr
    ▼
cell-region statistics
    │
    ▼
mask + two colors per cell
    │ Color Look + Mix
    ▼
canonical CellFrame
    ├── ANSI projection ──► Windows Terminal
    ├── direct raster ────► PNG
    └── CellTape ─────────► MP4 / GIF after Stop
```

## Camera mode selection

Camera enumeration and capture use Windows Media Foundation. Devices are tracked by their stable symbolic ID instead of display name alone.

The default request is 1920×1080 at 30 FPS. The selected device must expose at least one native MJPEG (`MJPG`) mode. When the exact request is absent, compatible-mode selection is deterministic:

1. Prefer frame rates within 1 FPS of the request. This treats 30000/1001, or about 29.97 FPS, as a 30 FPS mode.
2. Then prefer the closest aspect ratio.
3. Then prefer the closest pixel area and frame rate.
4. Use a faster mode before a slower mode when neither is near the requested cadence; a faster source can be cadence-limited, while a slower source cannot create missing frames.

The selected device never changes silently, and Media Foundation must confirm the same native MJPEG mode after negotiation. NV12/YUY2 conversion is not used.

On the tested Logitech C922, the app also constrains the Windows low-light behavior that can otherwise cut delivery below 30 FPS. The gain fallback is gated to the C922 device identity, and every changed camera control is restored when capture closes.

## Reduced decode and cell solving

MJPEG remains compressed until Microsoft WIC decodes it at quarter width and height (`lowres=2`). For a 1920×1080 source, the working planes are:

- luma: 480×270;
- Cb: 240×270; and
- Cr: 240×270.

That is 259,200 bytes of planar image data instead of 8,294,400 bytes for a full 1920×1080 RGBA image. Larger terminal grids may interpolate this reduced source, but do not claim detail that was discarded during decode.

The reducer computes the color/error statistics needed by the current terminal geometry. The Detailed solver evaluates the eight unique two-color 2×2 block partitions and selects the lowest-error mask. Softer modes use less spatial detail, but produce the same `CellFrame` type.

Geometry and configuration epochs prevent a frame produced for an old terminal size, crop, mirror, detail mode, or Color Look from being accepted after a setting changes.

## Color Looks

The built-in and custom `.cube` transforms are immutable, data-only color functions. A look is applied to the two solved colors in each cell, not to a second full-size image. **Mix** blends the transformed value with the original value from 0–100%.

This placement keeps the cost proportional to the terminal grid and guarantees that preview, snapshot, CellTape, and export use the same transformed colors. Changing a look starts a new configuration epoch, so an older frame cannot flash after the change.

See [FILTERS.md](FILTERS.md) for the accepted `.cube` subset and bounds.

## Latest-wins delivery

Live video uses bounded handoffs. If a consumer cannot keep up, stale work is released instead of queued behind the camera:

- native MJPEG packets use a fixed-capacity queue;
- decoded frames replace the oldest queued frame when that queue is full;
- the cell solver publishes into one latest-wins result slot; and
- the dashboard runtime uses one latest-wins preview slot.

Each replaced object returns to its pool. Memory and latency therefore remain bounded as the terminal slows down or the window is resized. Drop counts remain visible under **Details**.

The dashboard acknowledges a canonical frame only after it has composed the matching view version. A stale or evicted frame is not recorded. The accepted `CellFrame` is then the single source for the current ANSI preview, snapshots, and active CellTape.

The app targets 30 FPS and has no 24 FPS cap or health floor. Actual delivery is limited by the negotiated native mode and the slowest live stage. It does not invent unique frames.

## Display and pause

ANSI is only a projection of the canonical frame. Windows Terminal receives Unicode block masks and 24-bit foreground/background colors; the recorder does not parse terminal output or screen-record the window.

Static dashboard layout is cached, and a camera update replaces only the preview rows. Pausing holds the current canonical frame while the camera remains open. If recording is active, the held state remains valid for the paused duration.

## CellTape and export

An active recording writes accepted states to `recordings\.recovery\` as CellTape. The format uses timestamped keyframes and deltas, CRC32C checks, commit footers, bounded reusable buffers, and an approximately one-second durability window. Its memory use does not grow with recording duration.

When recording stops:

1. The tape is closed and published atomically.
2. The tape is replayed at the selected output cadence. A state may repeat when needed to preserve elapsed time.
3. Each canonical cell frame is enlarged directly to the selected 720p or 1080p canvas.
4. FFmpeg encodes the private stage and atomically publishes the completed media file.
5. The CellTape is removed only after a non-empty final file is confirmed.

MP4 uses H.264 with `yuv420p`, animation tuning, fast-start, and CRF 16 for High or CRF 20 for Standard. GIF first uses a lossless FFV1 stage, then an optimized palette with 256 colors for High or 192 for Standard. MP4 color coding is lossy and GIF is palette-limited, but both retain the canonical cell geometry and timing.

If export fails, the CellTape remains. On the next launch, recovery runs without blocking camera startup. It ignores tapes still owned by another process, validates resource bounds and the first-frame configuration, removes only a damaged suffix after the final valid commit, and avoids duplicate export when a completed destination already exists.

## Resource and trust boundaries

- Camera dimensions, cadence, packet size, queue depth, terminal geometry, tape chunks, settings, and `.cube` inputs are bounded before allocation.
- Frame, packet, and cell buffers have explicit ownership and return to fixed pools.
- Device IDs and file paths are passed as structured process arguments; media values are not assembled into shell command strings.
- FFmpeg runs only for offline export after Stop or recovery, not in the live camera loop.
- Camera frames, tapes, and exported files remain local.

The reference C922 system sustained about 29.83 source FPS for a three-minute native/WIC run. That measurement is a hardware result, not a guarantee for other cameras, terminal grids, or machines.
