# Color looks

Put trusted `.cube` LUT files directly in this folder, then restart Inlaid. They appear in the clickable **Color look** selector beside the built-in None, Warm, Cool, and Mono looks.

Inlaid accepts bounded Adobe/IRIDAS and DaVinci Resolve Cube data. It does not execute shaders, scripts, FFmpeg filter graphs, file references, or network requests. A look changes the two colors in each already-solved terminal cell, so preview and exports use the same filtered cells without adding spatial detail.

Use display-referred sRGB/Rec.709 looks. `.cube` files do not identify their color space, so camera-log or HDR LUTs can look wrong. See [`docs/FILTERS.md`](../docs/FILTERS.md) for the supported format and safety limits.
