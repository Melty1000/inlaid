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

The terminal-first Windows installer keeps settings and recovery data under the current user's local application-data directory and saves recordings, snapshots, filters, and support reports under the corresponding user media or documents folders. A portable or source run keeps its data beneath its explicitly resolved portable or source root. Inlaid has no account system, telemetry, update service, or media upload path.

The installed application and terminal-first portable ZIP do not download FFmpeg or any other tool. They accept a user-provided executable through `INLAID_FFMPEG` or `PATH`; the portable layout may also use `.tools\ffmpeg\bin\ffmpeg.exe` beneath its own root. Source setup may download Go modules and may run the repository's pinned, checksum-verifying FFmpeg setup helper. No camera frame is sent with those requests.

FFmpeg is external software and is not included in Inlaid release archives. The already-published legacy `v0.2.0-beta.1` Windows launcher may call its packaged `scripts\install-ffmpeg.ps1`; that historical helper downloads the named archive over HTTPS, verifies its fixed SHA-256 digest before extraction, and installs only `ffmpeg.exe` under the extracted release's `.tools\ffmpeg` directory. Current Linux and macOS source builds do not have an automatic FFmpeg installer.

## Input boundaries

- Camera devices use a platform identity: a Media Foundation symbolic ID on Windows, a V4L2 device identity on Linux, or an AVFoundation unique ID on macOS. Native mode negotiation is verified before frames are accepted.
- `.cube` looks are numeric data, not shaders or scripts. The parser accepts only direct regular files and enforces file, line, table, value, count, and aggregate-memory limits.
- Terminal dimensions, camera packets, decoded frames, recording queues, CellTape records, recovery candidates, and settings values are bounded before large allocations.
- FFmpeg receives a fixed executable path and structured argument list. User-visible media values are not evaluated by a shell.
- Recording stages are private until a complete output is validated and published. Recovery uses CRC-checked commits and process locks; it does not claim a tape still owned by another running instance. Unix file locks are advisory, so an unrelated process that ignores them can still interfere with files it can write.

These controls reduce risk but do not make arbitrary third-party cameras, drivers, LUTs, FFmpeg builds, or media files trustworthy.

## Local file considerations

The app does not encrypt settings, compatible legacy settings, snapshots, recordings, recovery data, custom filters, or support reports in any layout. Anyone with access to the operating-system account or the resolved data directories may be able to read them. On Windows, these files inherit their user-directory access controls; Unix support reports are created with owner-only permissions. Delete local data yourself when it is no longer needed.

Recording intentionally has no time limit. CellTape recovery data and completed media continue growing until recording is stopped or storage reports an error; check available space before a long session.

The currently published legacy release binaries are unsigned. Download them only from the project's GitHub Releases page. `SHA256SUMS.txt` can detect a damaged or mismatched ZIP, but a checksum hosted with the same unsigned release does not independently authenticate the publisher. The terminal-first distribution is not ready for publication until its separate signing and provenance gates are satisfied.
