# Changelog

Notable changes are recorded here. Versioning follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0-beta.1] - 2026-08-23

Initial public beta.

### Added

- One-page mouse and keyboard dashboard for live camera, framing, mirror, terminal detail, Color Look, save settings, and transport controls.
- Windows Media Foundation capture with stable device identity and deterministic native-MJPEG compatible-mode selection.
- A 30 FPS target with rational 29.97 FPS mode handling and no 24 FPS cap or floor.
- Reduced WIC JPEG decode, bounded latest-wins frame delivery, and a shared canonical terminal-cell representation.
- Built-in Warm, Cool, and Mono looks plus bounded Adobe/IRIDAS and DaVinci Resolve `.cube` support with adjustable Mix.
- PNG snapshots and offline MP4/GIF export from the same canonical cells displayed in the terminal.
- Crash-recoverable CellTape recording with committed-tail repair and automatic retry on the next launch.
- Double-click launcher for Windows Terminal and PowerShell 7, plus checksum-verified one-time FFmpeg setup.
- Windows test, vet, known-vulnerability scan, build, and tagged beta-release workflows.
- Bounded settings and Color Look discovery, checksum-pinned media setup, and commit-pinned GitHub Actions.

### Known limitations

- Windows and a native-MJPEG Media Foundation camera are required.
- The release executable is unsigned.
- MP4/GIF export requires separately acquired FFmpeg.
- Recordings do not include audio.

[Unreleased]: https://github.com/Melty1000/inlaid/compare/v0.1.0-beta.1...HEAD
[0.1.0-beta.1]: https://github.com/Melty1000/inlaid/releases/tag/v0.1.0-beta.1
