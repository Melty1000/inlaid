# Security policy

## Supported version

Security fixes are provided for the newest published `0.x` release. This beta may change file formats and compatibility behavior between releases.

## Report a vulnerability

Use the repository's **Security → Report a vulnerability** form to open a private GitHub Security Advisory. Include the affected version or commit, operating system and version, reproduction steps, impact, and any proof-of-concept files needed to verify the report.

Do not publish exploit details, malicious `.cube` files, or private camera output in a public issue. If private reporting is unavailable, open a minimal public issue asking the maintainer to enable a private contact channel.

## Support reports are local

A support report is for ordinary compatibility and troubleshooting work, not vulnerability disclosure. Inlaid creates one only when the user asks. The JSON file stays on the local machine until the user reviews it and attaches it to an issue; Inlaid does not upload it.

Support reports are designed to exclude camera images, recordings, absolute paths, stable device identifiers, environment dumps, host names, usernames, and credentials. Review the file before sharing it anyway. Attachments on this public repository are public, including files added while drafting an issue. Use the private vulnerability-reporting process above if a report could expose a security problem or sensitive proof of concept.

## Data and network behavior

The packaged Windows app keeps settings, recovery tapes, saved media, and support reports in its extracted directory. Source builds keep them beside the selected settings file. Inlaid has no account system, telemetry, update service, or media upload path.

On a launch with no working FFmpeg, the launcher may make one attempt to download a pinned archive. Source setup also downloads Go modules. No camera frame is sent with those requests.

FFmpeg is external software and is not included in Inlaid release archives. The published Windows launcher may call `scripts\install-ffmpeg.ps1`, which downloads the named archive over HTTPS, verifies its fixed SHA-256 digest before extraction, and installs only `ffmpeg.exe` under `.tools\ffmpeg`. The app also accepts a user-provided executable through `INLAID_FFMPEG` or `PATH`. Current Linux and macOS source builds do not have an automatic FFmpeg installer.

## Input boundaries

- Camera devices use a platform identity: a Media Foundation symbolic ID on Windows, a V4L2 device identity on Linux, or an AVFoundation unique ID on macOS. Native mode negotiation is verified before frames are accepted.
- `.cube` looks are numeric data, not shaders or scripts. The parser accepts only direct regular files and enforces file, line, table, value, count, and aggregate-memory limits.
- Terminal dimensions, camera packets, decoded frames, recording queues, CellTape records, recovery candidates, and settings values are bounded before large allocations.
- FFmpeg receives a fixed executable path and structured argument list. User-visible media values are not evaluated by a shell.
- Recording stages are private until a complete output is validated and published. Recovery uses CRC-checked commits and process locks; it does not claim a tape still owned by another running instance. Unix file locks are advisory, so an unrelated process that ignores them can still interfere with files it can write.

These controls reduce risk but do not make arbitrary third-party cameras, drivers, LUTs, FFmpeg builds, or media files trustworthy.

## Local file considerations

The app does not encrypt `inlaid-settings.json`, the compatible legacy `webcam-settings.json`, `snapshots\`, `recordings\`, `recordings\.recovery\`, or `support-reports\`. Anyone with access to the operating-system account or installation directory may be able to read them. On Windows, these files inherit that directory's access controls; Unix support reports are created with owner-only permissions. Delete local data yourself when it is no longer needed.

Recording intentionally has no time limit. CellTape recovery data and completed media continue growing until recording is stopped or storage reports an error; check available space before a long session.

Release binaries are currently unsigned. Download them only from the project's GitHub Releases page. `SHA256SUMS.txt` can detect a damaged or mismatched ZIP, but a checksum hosted with the same unsigned release does not independently authenticate the publisher.
