# Color Looks and `.cube` files

Inlaid includes None, Warm, Cool, and Mono. Custom Color Looks use the data-only `.cube` format; shader files and executable filter syntax are not supported.

## Add a look

1. Close Inlaid.
2. Copy a trusted `.cube` file directly into `filters\` beside the launcher.
3. Start the app again.
4. Choose the file under **Color Look** and adjust **Mix** from 0–100%.

Subfolders and symbolic links are ignored. The displayed name comes from a safe `TITLE` value when present, otherwise from the filename. Duplicate names receive a numeric suffix. Invalid files are skipped; **Details** shows the parser error.

The transform changes the same two colors stored in each terminal cell. It therefore appears in the live preview, PNG snapshots, CellTape, MP4, and GIF without rerunning mask selection or creating spatial detail. If a look makes both cell colors equal, the now-invisible mask may be canonicalized.

## Supported `.cube` subset

The parser accepts Adobe/IRIDAS and DaVinci Resolve-style files containing a 1D table, a 3D table, or a 1D shaper followed by a 3D table.

Supported headers:

- `TITLE "Name"`
- `LUT_1D_SIZE` from 2 through 65,536
- `LUT_3D_SIZE` from 2 through 65
- paired `DOMAIN_MIN` and `DOMAIN_MAX`
- `LUT_1D_INPUT_RANGE`
- `LUT_3D_INPUT_RANGE`

Adobe `DOMAIN_*` headers cannot be mixed with Resolve `*_INPUT_RANGE` headers. Each declared table must contain exactly the required number of RGB rows. For 3D tables, red is the fastest-changing axis. One-dimensional sampling is linear; three-dimensional sampling is tetrahedral.

Files may use UTF-8, a BOM at the beginning, blank lines, and `#` comments. Unknown headers, non-finite numbers, control characters, rows with the wrong number of values, and headers placed after table data are rejected.

## Bounds

- 16 MiB maximum per file
- 250 bytes maximum per line
- 128 custom looks maximum
- 512 direct folder entries scanned
- 64 MiB aggregate retained table data
- finite values within `-1e37..1e37`

These are parser limits, not recommended working sizes. A conventional 17³, 33³, or 65³ 3D LUT is sufficient.

## Color-space guidance

The camera colors entering the LUT are normalized display RGB values. `.cube` files do not identify their intended color space, so choose a look designed for display-referred sRGB/Rec.709 material. A LUT intended to convert Log, raw, HDR, or a specific cinema camera profile will usually produce clipped or incorrect color here.

At 100% Mix, final values are clamped to the terminal's 8-bit RGB range after the complete 1D/3D transform. Lower Mix values blend the transformed result with the original cell color.

Only use LUTs from sources you trust. They are parsed as bounded numeric data and never executed, but an unknown file can still produce misleading or unusable color.
