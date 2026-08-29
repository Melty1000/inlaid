# Roadmap

Inlaid is a public beta. The roadmap records direction, not delivery dates. GitHub Issues hold concrete work and compatibility evidence; a release is cut only from a commit that passes its native checks.

## Now: complete platform contracts, then distribute the proven Windows build

The current numbered sequence begins with the Linux and macOS contracts. Windows
implementation resumes in Phase 3; the bullets below describe the accepted
Windows outcome that those gates precede.

- Re-contract the Windows distribution around a terminal-first entry point: users install Inlaid, open their chosen Windows terminal and shell, and run `inlaid` from the user `PATH`. WSL Bash is a separate opt-in interoperability characterization that invokes the Windows program as `inlaid.exe`; it does not prove the bare-`inlaid` contract.
- Remove the installed Start-menu shortcut, Explorer launch support, app-owned Windows Terminal lookup and handoff, relaunch markers, and route-only reporting and tests. Do not replace them with another installed launcher or an in-app terminal picker; preserve the explicit source-development launcher behavior.
- Keep a signed x64 per-user MSI as the canonical artifact, with WinGet as the intended discovery and update channel and a portable ZIP as the secondary manual-update artifact.
- Make user-`PATH` ownership, new-terminal refresh, collision behavior, repair, upgrade, downgrade blocking, rollback, uninstall, and user-data preservation explicit and testable.
- Before publication, complete the applicable finite Windows host-and-shell matrix in [TESTING.md](TESTING.md), recording unavailable availability-gated rows without claiming them; classify only evidence-backed hosts in [COMPATIBILITY.md](COMPATIBILITY.md). Windows Terminal is the current physical baseline, not a runtime prerequisite.
- Keep release packages minimal, auditable, and reproducible from a verified commit.
- Keep the installed, portable, source, and explicit-test runtime layouts and the release-payload seam independent of the terminal, shell, installer, and package-manager layers.
- Use the distinct [Linux distribution contract](DISTRIBUTION-LINUX.md) for Linux packaging and evidence and the distinct [macOS distribution contract](DISTRIBUTION-MACOS.md) for macOS policy. Do not publish or claim support for either platform before physical acceptance.

## Platform distribution contract gate

Before a new Windows distribution is published, Windows, Linux, and macOS must
each have a distinct contract accepted by Cody. The
[Windows contract](DISTRIBUTION.md) is accepted. The
[Linux contract](DISTRIBUTION-LINUX.md) was accepted by Cody on 2026-08-26. The
[macOS contract](DISTRIBUTION-MACOS.md) was accepted by Cody on 2026-08-27.
Acceptance of any contract settles policy only; it does not
authorize another platform's implementation, validation, signing, or
publication.

## Revised distribution sequence

The accepted [Windows distribution contract](DISTRIBUTION.md) is a completed
legacy checkpoint, not Phase 1 of this revised sequence. It remains authoritative
while the two remaining platform contracts are settled before Windows
publication:

1. **Phase 1 — Linux contract (accepted 2026-08-26):** settle and accept the distinct durable Linux distribution and evidence requirements in the five authoritative documents.
2. **Phase 2 — macOS contract (accepted 2026-08-27):** settle and accept a distinct durable macOS distribution and evidence contract without importing Windows or Linux policy by implication.
3. **Phase 3 — Windows implementation:** reconcile code, packaging, tests, workflows, and user documentation with the accepted terminal-first Windows contract and complete local automated and MSI lifecycle checks.
4. **Phase 4 — Windows terminal-and-shell validation:** execute the finite available host baselines, representative shell rows, and separately authorized interoperability characterizations; fix and retest defects or classify them without overclaiming unavailable rows.
5. **Phase 5 — Verified Windows release candidate:** produce the immutable candidate, complete trusted signing and signature verification, checksums, attestations, exact payload reconciliation, and WinGet manifest validation and lifecycle rehearsal.
6. **Phase 6 — Windows publication and verification:** after separate authorization, publish and independently verify the GitHub release and any WinGet submission. Publication never starts from Phase 4 evidence alone.

## Ongoing: broaden physical evidence

- Collect physical-camera reports from Linux and macOS users. Native CI is necessary for a positive hardware-verified or public-binary claim, but it is not hardware verification. Its absence does not erase a bounded source-evidence classification such as **Expected—unverified** or **Known incompatible**.
- Expand Windows evidence beyond the first verified camera and terminal.
- Fix platform failures only against recorded OS, camera, terminal, cadence, memory, export, and shutdown evidence, then require affected-platform retesting.
- Treat unavailable outside hardware as deferred community validation, not as work assigned to the Windows maintainer and not as a blocker for unrelated roadmap phases.

## Next: refine the terminal experience

- Let users reduce or hide controls without shrinking the camera grid.
- Explore a separate control view and camera view where a terminal supports panes or tabs, while keeping the single-process view portable.
- Make controls remain legible when the terminal font is zoomed far out for a denser image.
- Refine borders, focus, mouse feedback, status messages, and compact layouts against real terminal captures.

## Then: add features from evidence

- Give document-free builds to fresh testers and record who Inlaid helps, where it fails, and which jobs it performs well.
- Prioritize features that strengthen those uses. Possible work includes more capture formats, more camera controls, richer terminal capability handling, and additional export options.
- Keep preview, snapshots, and recordings derived from the same terminal-cell representation. Saved files should not invent detail that the terminal did not show.

## Already established

- One canonical bounded cell representation feeds the preview, PNG, MP4, GIF, and recoverable CellTape recording.
- Preview delivery is latest-wins rather than an accumulating latency queue.
- Installed Windows use is terminal-first: after installation and opening a fresh Windows terminal host process and shell, users run `inlaid` from the user `PATH`; portable users run the executable from its extracted directory. WSL interoperability uses `inlaid.exe`. Double-click launch is not a supported product contract, and Inlaid never selects or launches a terminal host.
- The Windows MSI owns only its program files, uninstall registration, installer-private PATH provenance marker, and a PATH segment that marker proves it created. Uninstall fails safe by leaving ambiguous or pre-existing PATH text untouched and never removes settings, recordings, snapshots, recovery artifacts, custom filters, or support reports.
- Opt-in support reports are local, bounded, allowlisted, and never uploaded by Inlaid.
- Compatibility reports, issue forms, release notes, native CI, and an evidence-based support matrix are part of the repository.
- A Logitech C922 in Windows Terminal is the first hardware-verified setup.
- The recorded portability review at commit `adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3` and native CI run `32782751639` reports passes for Linux V4L2 and macOS AVFoundation capture paths; those claims apply only to that immutable evidence and both paths remain experimental until physical-camera evidence supports a stronger claim.

See [COMPATIBILITY.md](COMPATIBILITY.md) for current claims and [TESTING.md](TESTING.md) for the evidence needed to change them.
