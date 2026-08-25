# Native portability local review

This review covers the complete portability delta from `v0.1.0-beta.1` to
`v0.2.0-beta.1` (`5505639e578bde1597576a9cb1f0b76f68344eba..adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3`):
one squash commit, 129 changed paths. It is the Windows-machine closure review
for the native-portability contract in [PHASE_2.md](../PHASE_2.md), not physical
Linux or macOS camera acceptance.

## Standards review

The repository rules in `CONTRIBUTING.md` and `SECURITY.md` were checked as a
separate pass across:

- workflows, release scripts, launchers, and documentation;
- the shared capture types, packet and frame ownership, latest-wins queues, and
  close deadlines;
- Windows Media Foundation and WIC, Linux V4L2 and TurboJPEG, and macOS
  AVFoundation and NV12 boundaries;
- the shared cell pipeline, dashboard, recording, FFmpeg, and recovery paths;
- settings, local support reports, and their public issue handoff; and
- tests for mode selection, bounds, concurrency, shutdown, output identity, and
  privacy.

One documentation finding was supported: `PHASE_2.md`, `COMPATIBILITY.md`, and
`ROADMAP.md` still described native CI as merely configured or open even though
the release commit's [native Windows, Linux, and macOS jobs had passed](https://github.com/Melty1000/inlaid/actions/runs/32782751639).
The status wording now separates passed native CI from still-missing physical
Unix camera evidence.

No unresolved Standards finding remains. In particular, the reviewed paths keep
packet, planar-frame, canonical-frame, preview, recording, recovery, and report
storage bounded; retain and release displaced latest-wins values; keep full-canvas
rendering and FFmpeg work off the live capture loop; use structural FFmpeg
arguments; and preserve the explicit local-only support-report boundary.

## Spec review

The Phase 2 requirements were checked independently of the Standards pass:

| Requirement | Reviewed implementation and evidence | Result |
|---|---|---|
| Native platform boundary | Media Foundation/WIC on Windows, V4L2/MMAP/TurboJPEG on Linux, AVFoundation/NV12 on macOS | Satisfied in source and native CI; Unix hardware remains external |
| Exact device and mode | Stable opaque device IDs, common mode ranking, post-open negotiation checks | Satisfied in source; physical Unix behavior remains external |
| Owned bounded frames | `capture.Packet`, `capture.Frame`, platform pools, canonical `CellFrame`, latest-wins delivery | Satisfied |
| Safe lifecycle | Callback or worker admission stops before teardown; queued leases drain; uncertain shutdown blocks reopen | Satisfied in source and tests; physical removal and ownership cases remain external |
| Windows behavior | Shared solver, dashboard controls, CellTape, exports, settings, and terminal projection remain common | No portability review finding |
| Tester handoff | Normal app, explicit two-step report creation, one bounded local JSON, manual review and issue submission | One discoverability finding repaired |

The tester-handoff finding was that a successfully saved support report did not
become the dashboard's latest output, so **Open Folder** could still open a prior
media directory. The runtime now treats the report as the latest saved output,
the success notice explains that **Folder** opens its location, and an automated
test covers the path without weakening the report's privacy checks.

No unresolved Spec finding remains within Windows-verifiable scope.

## Evidence boundary and remaining acceptance

Native CI compiles and tests the actual cgo-backed Linux and macOS bridges; a
cgo-disabled cross-build is not treated as equivalent evidence. Windows can
verify the shared behavior, Windows capture, package, and report workflow. It
cannot prove Linux V4L2 or macOS AVFoundation camera cadence, memory behavior,
terminal presentation, permission prompts, removal, another-application
ownership, or close and reopen on physical devices.

Those physical cases remain the only separate acceptance stage. Testers should
follow [TESTING.md](../TESTING.md), create the local report only if useful, review
it, and manually submit the public [compatibility report](https://github.com/Melty1000/inlaid/issues/new?template=02-compatibility-report.yml).
