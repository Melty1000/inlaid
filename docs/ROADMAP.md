# Roadmap

Inlaid is a public beta. The roadmap records direction, not delivery dates. GitHub Issues hold concrete work and compatibility evidence; a release is cut only from a commit that passes its native checks.

## Now: prove the native builds

- Collect physical-camera reports from Linux and macOS users. Native CI is necessary, but it is not hardware verification.
- Fix platform failures against recorded OS, camera, terminal, cadence, memory, export, and shutdown evidence.
- Expand Windows evidence beyond the first verified camera and terminal.

## Next: distribute each proven platform

- Decide platform packaging from working native conventions: a signed Windows package, a macOS app and signing path, and appropriate Linux packages. Do not ship a universal installer merely for symmetry.
- Document supported launch methods for common terminals and shells without making the rendering core depend on either.

## Then: refine the terminal experience

- Let users reduce or hide controls without shrinking the camera grid.
- Explore a separate control view and camera view where a terminal supports panes or tabs, while keeping the single-process view portable.
- Make controls remain legible when the terminal font is zoomed far out for a denser image.
- Refine borders, focus, mouse feedback, status messages, and compact layouts against real terminal captures.

## Later: add features from evidence

- Give document-free builds to fresh testers and record who Inlaid helps, where it fails, and which jobs it performs well.
- Prioritize features that strengthen those uses. Possible work includes more capture formats, more camera controls, richer terminal capability handling, and additional export options.
- Keep preview, snapshots, and recordings derived from the same terminal-cell representation. Saved files should not invent detail that the terminal did not show.

## Already established

- One canonical bounded cell representation feeds the preview, PNG, MP4, GIF, and recoverable CellTape recording.
- Preview delivery is latest-wins rather than an accumulating latency queue.
- Windows Explorer launches are handed to Windows Terminal without inserting a shell; launches from another terminal stay in that terminal.
- Opt-in support reports are local, bounded, allowlisted, and never uploaded by Inlaid.
- Compatibility reports, issue forms, release notes, native CI, and an evidence-based support matrix are part of the repository.
- A Logitech C922 in Windows Terminal is the first hardware-verified setup.
- Linux V4L2 and macOS AVFoundation capture paths exist but remain experimental until native CI and physical-camera evidence say otherwise.

See [COMPATIBILITY.md](COMPATIBILITY.md) for current claims and [TESTING.md](TESTING.md) for the evidence needed to change them.
