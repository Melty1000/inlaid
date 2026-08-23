# Security policy

## Supported version

Security fixes are provided for the newest published `0.x` release. This beta may change file formats and compatibility behavior between releases.

## Report a vulnerability

Use the repository's **Security → Report a vulnerability** form to open a private GitHub Security Advisory. Include the affected version, Windows version, reproduction steps, impact, and any proof-of-concept files needed to verify the report.

Do not publish exploit details, malicious `.cube` files, or private camera output in a public issue. If private reporting is unavailable, open a minimal public issue asking the maintainer to enable a private contact channel.

## Data and network behavior

Camera frames, settings, recovery tapes, and saved media remain in the extracted project directory. Inlaid has no account system, telemetry, update service, or media upload path.

On a launch with no working FFmpeg, the launcher may make one attempt to download a pinned archive. Source setup also downloads Go modules. No camera frame is sent with those requests.

FFmpeg is external software and is not included in Inlaid release archives. `scripts\install-ffmpeg.ps1` downloads the named archive over HTTPS, verifies its fixed SHA-256 digest before extraction, and installs only `ffmpeg.exe` under `.tools\ffmpeg`. The app also accepts a user-provided executable through `INLAID_FFMPEG` or `PATH`.

## Input boundaries

- Camera devices are selected by their Media Foundation symbolic ID, and native mode negotiation is verified before frames are accepted.
- `.cube` looks are numeric data, not shaders or scripts. The parser accepts only direct regular files and enforces file, line, table, value, count, and aggregate-memory limits.
- Terminal dimensions, camera packets, decoded frames, recording queues, CellTape records, recovery candidates, and settings values are bounded before large allocations.
- FFmpeg receives a fixed executable path and structured argument list. User-visible media values are not evaluated by a shell.
- Recording stages are private until a complete output is validated and published. Recovery uses CRC-checked commits and process locks; it does not claim a tape still owned by another running instance.

These controls reduce risk but do not make arbitrary third-party cameras, drivers, LUTs, FFmpeg builds, or media files trustworthy.

## Local file considerations

The app does not encrypt `inlaid-settings.json`, the compatible legacy `webcam-settings.json`, `snapshots\`, `recordings\`, or `recordings\.recovery\`. Anyone with access to the Windows account or extracted directory may be able to read them. Delete those files yourself when they are no longer needed.

Recording intentionally has no time limit. CellTape recovery data and completed media continue growing until recording is stopped or storage reports an error; check available space before a long session.

Release binaries are currently unsigned. Download them only from the project's GitHub Releases page. `SHA256SUMS.txt` can detect a damaged or mismatched ZIP, but a checksum hosted with the same unsigned release does not independently authenticate the publisher.
