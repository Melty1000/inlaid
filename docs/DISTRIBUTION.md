# Distribution contract

Status: **Accepted by Cody on 2026-08-26**

Applies to: Windows distribution after `v0.2.0-beta.1`

Decision owner: Cody

This document settles the installation, update, launch, data, packaging, and
future-platform boundaries needed before Inlaid changes its published Windows
distribution. It does not authorize implementation or publication.

This accepted document remains the Windows-owned contract. The separate
accepted [Linux distribution contract](DISTRIBUTION-LINUX.md) replaces only the
provisional future-Linux and generic non-Windows placeholders identified there;
it does not amend, weaken, or reinterpret any Windows decision here.

The [macOS distribution contract](DISTRIBUTION-MACOS.md) is a separate
macOS-owned contract accepted by Cody on 2026-08-27. Its acceptance changes no
Windows guarantee, implementation obligation, support claim, or publication
authority here.

## Superseded published baseline

The immutable baseline for the distribution being superseded is release
`v0.2.0-beta.1`, commit
[`adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3`][v020-commit]:

- Its Windows artifact is a ZIP whose instructions start with
  `START-INLAID.cmd`; writable settings and outputs remain under the extracted
  root.
- Its [PowerShell launcher][v020-launcher] combines source building, optional
  FFmpeg setup, package/source markers, Windows Terminal startup, and process
  launch.
- Its [Windows startup policy][v020-startup] distinguishes a plain Explorer
  parent from an existing terminal and hands Explorer launches to Windows
  Terminal without a shell.
- Its [ZIP packager][v020-packager] uses a file-by-file allowlist, and its
  [release workflow][v020-release-workflow] rebuilds and publishes from a tag.

Those release-pinned behaviors are superseded where this contract says so. A
mutable working tree, provisional implementation, or passing build is not
historical evidence and cannot accept this contract. Implementation evidence
must instead show that source, packaging, workflows, tests, and user
documentation conform to the accepted policy.

## Decision

Inlaid will use one canonical Windows release artifact and one package-manager
front end:

1. A **signed, per-user x64 MSI** built with a pinned WiX Toolset release is the
   canonical Windows artifact. It installs without administrator rights,
   registers an uninstall entry, makes `inlaid` available to fresh native
   Windows shell processes through the current user's `PATH`, and supports
   unattended install, repair, upgrade, and uninstall. It creates no Start menu
   shortcut or other graphical launcher.
2. **WinGet** is the default discovery and update path. Its manifest points to
   the exact MSI already published on the Inlaid GitHub release; WinGet never
   causes a second installer build.
3. The **portable ZIP remains supported as a secondary option** for removable,
   isolated, and package-manager-free use. It is not the first-run path in the
   README and it has manual updates.
4. Inlaid has **no in-app updater**. Installed builds do not poll a server or
   download executables. WinGet users opt into updates with WinGet; direct-MSI
   users run the newer MSI; portable users manually reconcile only
   release-owned payload files as defined by the portable-update contract below.
5. The Microsoft Store, MSIX/App Installer, Chocolatey, and Scoop are deferred.
   They can be reconsidered from real demand, but they are not parallel release
   obligations for the Windows maintainer.

Microsoft's current Windows guidance recommends Store/MSIX for many new apps
because the Store supplies signing and updates, while also describing MSI and
WiX as established choices and WinGet as an update/discovery layer for technical
audiences. The recommendation above is project-specific: Inlaid is an existing
unpackaged Go terminal application whose normal interface is an interactive
terminal, and its release authority is an immutable GitHub artifact. MSI plus
WinGet improves installation without making one terminal host part of runtime
launch policy. See Microsoft's [distribution-path comparison][distribution-path]
and [WinGet manifest guidance][winget-manifest].

## Option comparison

| Path | What it gives Inlaid | Decisive cost or mismatch | Decision |
|---|---|---|---|
| Current portable ZIP | Small, transparent, easy to inspect and roll back by keeping the old folder | No registered install or upgrade path; users manage extraction, shortcuts, and versions; writable data is mixed with program files | Retain as secondary |
| WinGet portable ZIP | Package-manager download, command shim, update, and uninstall without an installer | Remains a portable layout and does not use the canonical MSI upgrade and repair contract | Not the default |
| Signed MSI + WinGet | Registered per-user install, user-`PATH` command, silent modes, Windows Installer upgrade semantics, and one artifact shared by direct download and WinGet | Requires installer authoring, trusted signing, a data-root migration, PATH ownership, and Windows upgrade testing | **Selected** |
| MSIX + Store or App Installer | Package identity, clean package ownership, execution aliases, and Store/App Installer updates | Adds a different activation and install-location boundary; direct MSIX also needs trusted signing; Store submission adds a separate certification channel | Defer |
| Chocolatey | Mature packaging with shims and automation | Adds a NuGet/PowerShell package and separate community publication lifecycle around the same MSI | Defer |
| Scoop | Excellent portable command shims, persisted data, and manifest autoupdate | Creates a second package-manager lifecycle instead of using the canonical MSI | Defer |

WinGet accepts MSI and other established installer types, correlates MSI product
codes for upgrades, and requires public submissions to install and uninstall
unattended from a publisher-controlled URL. Its repository also scans and
validates submissions. See the [manifest format][winget-manifest], [repository
expectations][winget-repository], and [community policies][winget-policies].
Chocolatey's own package documentation describes the additional NuGet metadata
and PowerShell automation it maintains, while Scoop's manifest documentation
describes its portable shims and persisted-data model. Those are useful later
channels, not reasons to multiply release work now. See [Chocolatey package
creation][chocolatey] and [Scoop manifests][scoop].

## Installer contract

### Scope and ownership

- The MSI is x64 and **strictly per-user**. Its program directory is
  `FOLDERID_UserProgramFiles\Inlaid`, normally
  `%LOCALAPPDATA%\Programs\Inlaid`. WiX 5 has no legal standard-directory ID
  for the per-user Programs folder, so the package backports the WiX 6 shape:
  an ordinary `Directory` named `Programs` under the legal
  `LocalAppDataFolder` standard directory, with `INSTALLFOLDER` beneath it.
  It does not globally override `ProgramFiles64Folder`. The package captures
  the resolved `INSTALLFOLDER` after costing for PATH ownership, declares
  per-user scope, and does not author the dual-purpose
  `ALLUSERS=2` / `MSIINSTALLPERUSER=1` controls. See
  [known folders][known] and [MSI per-user installation][msi-per-user].
- ICE validation narrowly suppresses the WiX 5 backport's expected ICE64
  report for the shared `PerUserProgramFilesFolder` node. A separate ICE64-only
  evidence pass must report exactly that node. Uninstall removes only the
  empty Inlaid-owned `INSTALLFOLDER`, `docs`, and `filters` directories; it
  never authors a `RemoveFolder` row for the shared `%LOCALAPPDATA%\Programs`
  parent.
- No elevation is required for the normal install. A per-machine variant is not
  part of the first professional distribution.
- The MSI owns only installed program files, its Windows Installer registration,
  installer-private PATH provenance state, and a user-`PATH` segment that state
  proves it created. Uninstall removes only proven-owned resources. It never
  deletes settings, recordings, snapshots, recovery data, custom filters,
  support reports, or ambiguous or pre-existing PATH text.
- Ordinary Windows Installer/WiX tables remain the default. A narrowly scoped,
  per-user, rollback-aware installer helper is allowed only for the PATH
  comparison and provenance transaction defined below, because the standard MSI
  Environment-table append/remove behavior does not record whether an equivalent
  segment predated installation. The helper never elevates, launches Inlaid or a
  shell, accesses the network, or changes unrelated environment or registry
  state. See the Windows Installer [Environment table][msi-environment] and WiX
  [Environment element][wix-environment].
- Deferred helper actions receive the package's validated `UserSID` and address
  that already-loaded hive through `HKEY_USERS\<sid>`; they do not rely on the
  deferred Windows Installer server's `HKEY_CURRENT_USER` mapping. Rollback
  persists the same SID with the exact raw PATH/value-kind and provenance
  snapshot before any write. Each product uses its own ProductCode-qualified
  transaction snapshot. The upgrading-away product neither schedules a PATH
  mutation nor rollback/commit cleanup, so it cannot consume the incoming
  product's snapshot during a major upgrade. Because ProductCode is release
  identity rather than transaction identity, an immediate preflight rejects an
  existing product snapshot or partial snapshot before any rollback action is
  scheduled; source ordering explicitly chains preflight, the private
  transaction guard, rollback-data setup, and rollback scheduling. Snapshot
  publication exclusively creates a no-follow partial file and atomically moves
  it without replacement. Neither an existing partial nor an existing or racing
  final snapshot is truncated, removed, or overwritten.
  The claim itself moves through `preflight`, `active`, and `cleanup` phases.
  `preflight` is non-mutating: rollback may remove a preflight claim with no
  snapshot, and may idempotently verify and restore a valid authenticated
  snapshot if publication completed before activation. Only after snapshot
  publication may the helper activate the claim and begin registry mutation.
  An active claim without its authenticated snapshot fails closed. Teardown
  enters `cleanup` before deleting the snapshot, and a later serialized
  preflight finishes valid cleanup residue before exclusively creating a fresh
  claim; it never consumes active state.
- The package has a `NOT RollbackDisabled` launch condition. It refuses install,
  repair, upgrade, or uninstall when Windows Installer rollback is disabled,
  because install, repair, upgrade, and pre-execution uninstall protection depend
  on an available rollback transaction.

### Entry points

- `inlaid.exe` is installed directly under the program directory. The installed
  layout does not add a `bin` layer.
- The installer adds the program directory to the current user's `PATH` so
  `inlaid` works in fresh terminal processes opened after installation. It does
  not create a Start menu shortcut, desktop shortcut, PowerShell wrapper,
  Command Prompt wrapper, batch wrapper, protocol handler, or file association.
- The installed payload excludes `START-INLAID.cmd` and `START-INLAID.ps1`.
  Those files remain source-development conveniences only and are never installed
  or included in the portable ZIP. The `.ps1` file remains responsible for
  source build/setup, optional pinned FFmpeg setup, and passing the explicit
  source data root to the executable. Neither file owns installed launch policy.
- Installed use is terminal-first: install Inlaid, close every process of the
  terminal host being tested, open a fresh Windows terminal and native Windows
  shell of the user's choice, and run `inlaid`. Explorer double-click and
  Start-menu activation are not supported product entry points. WSL Bash is the
  separate interoperability route defined below and invokes `inlaid.exe`.
- The application never looks up, launches, or hands off to Windows Terminal or
  another host. The rendering core remains unaware of terminal hosts, shells,
  WinGet, MSI, and WiX.

### User PATH ownership, refresh, collision, and repair

- The current user's PATH is parsed only as a semicolon-delimited list. Existing
  segment text, empty segments, quoting, variables, separators, and order are
  preserved byte for byte. The MSI never changes the machine PATH.
- For equality only, each non-empty segment is normalized by trimming surrounding
  whitespace, removing one matching pair of surrounding double quotes, expanding
  current-user environment variables once, requiring the result to be rooted,
  resolving `.` and `..` with the Windows full-path rules, and removing trailing
  `\` or `/` except at a volume root. Relative, empty, or invalid segments are
  foreign. Comparison with the fully resolved
  `FOLDERID_UserProgramFiles\Inlaid` path uses
  `StringComparison.OrdinalIgnoreCase`; normalization is never written back.
- The fully resolved program directory must not contain the PATH delimiter `;`.
  If it does, install, repair, or upgrade fails closed and the Windows Installer
  transaction rolls back before committing any PATH or provenance-marker
  change. The helper never quotes, escapes, splits, or partially compares such a
  directory because it cannot be represented as one Windows PATH segment.
- If no equivalent segment exists, first install appends the unquoted, fully
  resolved program directory after all existing user-PATH text and records that
  exact inserted text. An empty PATH becomes only that segment; a non-empty PATH
  becomes the original byte-for-byte text, one new semicolon, then the segment,
  even when the original ended with a semicolon so its trailing empty segment is
  preserved. The MSI never prepends, reorders, or broadly deduplicates PATH. If
  one or more equivalent segments already exist, install adds nothing and treats
  every such segment as user-owned.
- The per-user installer key `HKCU\Software\Inlaid\Installer` persists a schema
  version, the normalized program directory, the exact inserted segment text,
  an `owned` boolean, and whether the PATH value existed immediately before the
  transaction most recently acquired ownership. An already-owned exact segment
  preserves that prior-presence field; a repair or upgrade that re-appends a
  missing segment refreshes it from the then-current PATH state. An unowned
  marker has no inserted segment and no prior-presence claim. This marker is an
  installer component with stable identity across repair and major upgrade.
  PATH and marker changes are one rollback-aware transaction for install, repair,
  and upgrade. A detectable uninstall preflight conflict refuses the operation
  before the first execution script, leaving product registration, payload,
  marker, and PATH unchanged. These boundaries preserve PATH as absent when it
  was originally absent and preserve `REG_SZ` versus `REG_EXPAND_SZ`.
  Unexpected registry open, delete, or close failures fail the rollback and
  retain its snapshot for retry.
  On uninstall, the deferred PATH action clears only the known marker values,
  Windows Installer removes its `Components` values, and a normal deferred
  finalizer verifies the expected PATH state and absence of all known owned
  marker values while rollback is still available. It never deletes the empty
  `Components` or installer key: enumeration followed by key deletion cannot
  atomically exclude foreign content racing into that structure. Unknown values
  or subkeys are preserved and fail lifecycle evidence; any owned residue also
  fails lifecycle evidence.
  The commit action only consumes the authenticated snapshot and claim; it
  performs no PATH or registry mutation.
- Complete removal has a Windows Installer boundary: after the first execution
  script begins, Windows Installer may automatically remove product and
  Add/Remove Programs registration before authored deferred actions run. The MSI
  therefore does not claim exact full-product rollback after an unexpected late
  uninstall failure. User data remains outside MSI ownership, but reinstalling
  the same verified package may be required before uninstall can be retried. See
  Microsoft's [Add/Remove Programs registration behavior][msi-arp].
- Repair and upgrade preserve the marker. When `owned=true`, a missing exact
  inserted segment is re-appended unless an equivalent user-edited segment now
  exists; an equivalent but non-identical or duplicate segment is treated as
  user-owned and changes the marker to `owned=false`. When `owned=false` and no
  equivalent remains, repair or upgrade appends the canonical segment and changes
  the marker to `owned=true` because that transaction created it.
- Final uninstall removes one PATH segment only when `owned=true`, exactly one
  segment matches both the stored literal text and normalized program directory,
  and the transaction can remove it without rewriting any other segment. If the
  owned segment is the entire value, uninstall restores PATH as absent only when
  the prior-presence field proves it was absent when ownership was acquired;
  an originally present-but-empty PATH remains a present empty value. If the
  marker is absent, false, malformed, stale, or ambiguous, or duplicate
  equivalents exist, uninstall leaves PATH unchanged, records a bounded warning,
  and removes only installer-private marker state. Preserving ambiguous text is
  safer than claiming perfect cleanup without provenance.
- The installer broadcasts the ordinary environment-change notification, but
  already-running shells and terminal host processes may retain their inherited
  environment. A new tab in an old host process is not proof of refresh. The
  acceptance route closes every instance of the host, starts a new host process,
  opens the chosen shell, and checks command discovery there.
- The disposable MSI lifecycle harness does not automate that host-refresh
  claim. A child process inherits its parent's environment, while reconstructing
  `PATH` from the registry would only prove persisted state. Automated lifecycle
  evidence therefore stops at exact registry bytes/type and direct executable
  identity; fresh-host command discovery remains Phase 4 physical evidence.
- A different `inlaid` earlier on the effective `PATH` is a collision. Install,
  repair, upgrade, and uninstall never delete or reorder the competing program.
  Evidence records `where.exe inlaid` and the shell's native command-resolution
  result without publishing user paths. The user repairs the collision by
  removing, renaming, or reordering the competing installation, then starts a
  fresh terminal process. Windows Installer repair restores only MSI-owned files,
  registration, and an MSI-owned PATH segment; it cannot repair a foreign
  collision.
- A professional-distribution compatibility claim requires `inlaid` to resolve
  to the installed executable in a fresh native Windows shell process. A
  collision is useful failure evidence, not permission to claim the wrong
  executable passed. WSL interoperability separately resolves `inlaid.exe` and
  never counts as proof of bare-`inlaid` discovery.

### Version and upgrade identity

- The MSI has one permanent UpgradeCode. Every release receives a new
  ProductCode and PackageCode.
- WiX major-upgrade behavior commits the incoming product before it removes the
  older product, preserves user data, and blocks installing an older MSI over a
  newer one. A failure before `InstallFinalize` rolls back the incoming
  transaction while the older product remains installed. Old-product removal
  runs afterward as a separate Windows Installer operation. If that removal
  fails, the failure remains visible and the committed newer product remains;
  the older product or its registration may also remain and require separate,
  version-specific cleanup. Inlaid does not claim atomic two-product rollback across that
  boundary. See [WiX MajorUpgrade][wix-major-upgrade], Microsoft's
  [RemoveExistingProducts sequencing][msi-remove-existing], and
  [InstallFinalize transaction boundary][msi-install-finalize].
- Inlaid's public version remains the leading-`v` Git tag, such as
  `v0.3.0-beta.1`. Windows
  Installer compares only a three-part numeric ProductVersion, so packaging
  keeps a repository-tracked release ledger. Within each semantic major/minor
  line, the ledger assigns a monotonically increasing numeric build component:
  for example, Git tag `v0.3.0-beta.1` may map to MSI `0.3.1`, the next
  prerelease to
  `0.3.2`, and the stable release to the next unused number. The executable,
  executable identity, filenames, and release notes retain the leading-`v` Git
  tag; WinGet PackageVersion retains the corresponding semantic version without
  the tag prefix;
  WinGet's AppsAndFeatures entry records the MSI DisplayVersion used for
  correlation. The mapping is never inferred from CI run numbers and never
  reused. See the Windows Installer [ProductVersion limits][msi-version] and
  WinGet's [MSI correlation guidance][winget-manifest].
- A direct MSI update and a WinGet update execute the same major-upgrade path.
  Portable ZIPs never participate in MSI upgrade detection.
- Failed incoming MSI transactions before `InstallFinalize` rely on Windows
  Installer rollback. A failure while removing the older product after that
  boundary does not roll back the already committed newer product; evidence must
  prove the newer product remains repairable and any older registration is
  handled through a separate version-specific cleanup without touching user
  data. An intentional downgrade requires uninstalling the newer MSI first
  and installing the older retained release. Release notes must say when stored
  settings cease to be backward compatible; absent such a warning, one previous
  release must read the current settings file safely.

### Signing

- Every public installed release signs both `inlaid.exe` and the MSI with one
  consistent CA-trusted publisher identity and an RFC 3161 timestamp. The ZIP
  contains the same signed executable; the ZIP itself is covered by its SHA-256
  checksum and release attestation.
- Azure Artifact Signing is the preferred provider if Cody accepts its verified
  publisher identity, eligibility, and recurring cost. A conventional OV
  certificate or an accepted open-source signing service is the fallback. The
  provider choice is an external prerequisite for publication, not permission
  created by this document.
- Self-signed certificates are for disposable installer tests only and are
  never published. Unsigned public MSI/EXE artifacts are not a fallback.
- Signing credentials are never available to pull-request builds. The protected
  tag workflow receives narrowly scoped signing identity only after all native
  validation and packaging checks pass.
- Verification uses SignTool policy verification in addition to checking the
  artifact checksum and attestation. Microsoft documents that SignTool verifies
  trust, revocation, and signing policy; its current signing guidance recommends
  SHA-256 digests. See [code-signing options][signing-options] and
  [SignTool][signtool].

Trusted signing improves publisher identity but does not promise an immediate
absence of SmartScreen reputation prompts. The download and release notes must
state that accurately rather than claiming signing is a warning bypass.

## Application data contract

Installed program files are read-only application inputs. User-writable state
has explicit homes returned by the operating system's known-folder APIs:

| Data | Installed Windows path |
|---|---|
| Settings | `%LOCALAPPDATA%\Inlaid\inlaid-settings.json` |
| Recording recovery | `%LOCALAPPDATA%\Inlaid\Recovery` |
| Recordings | `FOLDERID_Videos\Inlaid`, normally `%USERPROFILE%\Videos\Inlaid` |
| Snapshots | `FOLDERID_Pictures\Inlaid`, normally `%USERPROFILE%\Pictures\Inlaid` |
| Custom `.cube` filters | `FOLDERID_Documents\Inlaid\Filters` |
| Support reports | `FOLDERID_Documents\Inlaid\Support Reports` |

The known-folder APIs, rather than concatenated environment strings, are the
authority because Windows allows Documents, Pictures, and Videos to be moved or
redirected. Microsoft defines these as per-user known folders in its
[KNOWNFOLDERID reference][known].

The installed application does not create or write `.tools` under its program
directory. It discovers FFmpeg through `INLAID_FFMPEG` or `PATH`. FFmpeg remains
unbundled, optional, and outside the live preview and PNG paths. The installed
application and MSI do not download FFmpeg. The existing pinned FFmpeg helper
may remain for source and portable users, but it is not an installed launch
dependency.

### Portable mode

The portable ZIP contains `inlaid-portable.json` in its root. That file is both
the portable marker and the versioned release-owned payload manifest. When that marker is present,
the executable keeps the existing colocated layout: settings, recordings,
snapshots, recovery, filters, support reports, and optional `.tools` stay under
the extracted root. The marker is the authority; the app does not guess from
the current working directory or a writable executable location.

The portable archive places `inlaid.exe` at its root and includes the portable
marker, user-facing README, license, third-party notices, filter instructions,
and only the optional helper files named by the portable payload allowlist. It
does not include source-build launchers by default. Portable use is also
terminal-first: open a terminal in the extracted directory and run
`.\inlaid.exe` (or an equivalent shell-relative command). Double-clicking the
executable is not a supported product contract.

A manual portable update reconciles release-owned payload files only. It adds or
replaces files in the new portable payload manifest and removes an obsolete file
only when the prior release manifest proves that file was release-owned. It
carries the portable marker authority and presence forward by atomically advancing
`inlaid-portable.json` to the exact new versioned manifest, and preserves all user-writable state byte-for-byte:
settings, recovery, recordings, snapshots, filters, support reports, and any
optional `.tools`. Deleting or replacing the entire portable root is not a
supported update procedure. Update instructions and evidence must use a staged
or otherwise recoverable copy so an interrupted update does not turn user data
into release payload.

Both the prior and next manifests must use the updater's fixed, explicit,
unique role-to-destination pairs. A target manifest must contain the exact
complete current portable profile; retired pairs are accepted only from a
prior manifest so their proven-owned files can be removed. A future
release-owned path requires a deliberate updater-contract change; recovery
never accepts an arbitrary path merely because transaction state and a locally
modified manifest agree.

### Source mode

Source trees retain their repository-local settings and output
behavior, but they request it explicitly. `START-INLAID.ps1` passes the project
root as the source data root when it starts the built executable. The executable
does not infer source mode merely because a `go.mod`, `.git` directory, writable
working directory, or development version string is nearby.

Until Linux or macOS packaging receives physical acceptance, each platform
adapter retains the explicit/current-working-directory source behavior. That
preserves development without pretending an installed layout has been accepted.

### Migration from the published ZIP

- Installation never scans disks, Downloads, Desktop, or arbitrary folders for
  an old Inlaid extraction.
- The new executable exposes an explicit import operation that receives one
  user-selected portable root. It validates that root, copies settings and
  custom filters into the installed locations without deleting the source, and
  reports every copied, skipped, or conflicting file.
- The published `v0.2.0-beta.1` ZIP predates the portable marker. Import accepts
  that one markerless baseline only when its direct release-owned file and
  directory shape matches the pinned published package and no source-tree or
  current-portable shape is also present. Validation reads no executable or
  script as code and launches nothing. Every later portable layout requires its
  direct regular portable marker; an arbitrary or ambiguous markerless folder
  is refused before any destination write.
- Recordings, snapshots, and support reports remain where the user put them;
  the import reports those paths instead of duplicating large or public-facing
  files.
- Import refuses to move or copy live recovery tapes. The user finishes or
  abandons recovery with the portable version first. The source folder remains
  recoverable throughout.
- Existing `--settings` behavior remains an explicit expert/test override. It
  does not silently convert an installed run into portable mode unless the
  portable marker or an explicit portable-root option is also present.

## Terminal-first invocation contract

The user chooses the terminal host and shell. Inlaid consumes the terminal it
was invoked from and never routes, relaunches, or inserts another process:

1. Installed users complete installation, open a fresh terminal host process
   and native Windows shell, and run `inlaid` through the effective user `PATH`.
2. Portable users open a terminal at the extracted root and run the executable
   by its shell-relative path. Source users use the documented source launcher
   or an explicit source root. Explicit tests use the explicit test root.
3. WSL Bash is not a native-shell entry point. When WSL is already enabled,
   Windows executable interoperability and Windows PATH import are active, an
   opt-in characterization may invoke `inlaid.exe`. Microsoft requires the
   `.exe` suffix for Windows tools launched from WSL; bare `inlaid` searches for
   a Linux program and does not prove installed command discovery. See
   [Microsoft's WSL interoperability guidance][wsl-interop].
4. The process keeps the caller's terminal and working directory. Data paths are
   resolved independently through the installed, portable, source, or
   explicit-test layout and never derive from an installed launch directory.
5. Explorer double-click, Start-menu activation, app-owned Windows Terminal
   lookup or handoff, relaunch markers, and launch-route reporting are absent.
   Attempting to start the interactive dashboard without a suitable interactive
   terminal fails usefully; it does not choose a host, fall back to Console Host,
   or insert a shell. Supported non-interactive command modes, including
   `--version` and deterministic `--render-preview`, remain available without an
   interactive terminal.
6. A host is compatible only when recorded physical evidence supports the
   claim. Windows Terminal is the only host with full physical camera and visual
   evidence today, but it is not a runtime prerequisite.

| Invocation | Contract |
|---|---|
| `inlaid` from a fresh native Windows shell | Primary installed entry point; bare-name command discovery, current host, and working directory are preserved |
| `inlaid.exe` from WSL Bash | Optional Windows-executable interoperability characterization only; requires existing or separately authorized WSL and does not prove bare `inlaid` |
| Shell-relative `inlaid.exe` from the portable root | Secondary portable entry point; manual updates and portable marker semantics apply |
| `START-INLAID.cmd` in a source tree | Source-development convenience only; invokes the PowerShell source launcher and explicit source data root and is absent from installed and portable payloads |
| Explorer double-click or Start-menu activation | Unsupported; no application-owned handoff or graphical launcher |
| A shell wrapper in an installed package | Absent |
| Console Host | Characterization target only, not a modern-support promise and never an automatic fallback |

MSIX can expose an app execution alias, but that would be a genuinely different
activation boundary. Microsoft's guidance is useful for a future MSIX
experiment, not permission to add another launch route to the MSI contract. See
Microsoft's [MSIX desktop preparation guidance][msix-alias].

## Packaging seams

Implementation uses two narrow, evidence-backed seams rather than teaching the
application about every distribution channel.

### Runtime layout seam

Resolve all file locations once through an application layout value:

```text
Layout
  ProgramRoot
  SettingsFile
  RecordingsDir
  SnapshotsDir
  RecoveryDir
  FiltersDir
  SupportReportsDir
  Mode = installed | portable | source | explicit-test
```

The Windows adapter resolves one mode in a fail-closed order: an explicit source
or test data root, then the portable marker, otherwise the installed known-folder
layout. `--settings` overrides only the settings file; it does not change the
mode or derive every other location from the settings directory. Existing
non-Windows source behavior remains behind its own adapter until later native
packaging work has physical acceptance. The dashboard runtime consumes the
resolved locations; it no longer receives one overloaded writable `root`.

The layout resolver reports `installed`, `portable`, `source`, or
`explicit-test`. Application-owned launch-route detection and reporting are
removed because terminal-first invocation has no product routing decision to
make. A bounded support report may record sanitized terminal and shell facts but
never the install root, working directory, executable path, user-data paths, or
an obsolete launcher marker.

This seam is justified by current variation, not hypothetical platforms: the
installed and portable Windows layouts already have incompatible mutability and
ownership rules. It also leaves one obvious adapter point for later XDG and
macOS Application Support conventions without changing capture or rendering.

### Release payload seam

A single repository-tracked payload manifest names every common installed file.
The ZIP packager, WiX authoring, package tests, checksum generation, and release
allowlist consume that manifest rather than maintaining independent file lists.
Channel-specific additions are explicit profiles:

- common: signed executable, license, required notices, user documentation;
- MSI: Windows Installer metadata, PATH comparison/provenance resources, any proven-owned user-`PATH` segment, and uninstall registration;
- portable: portable marker and explicitly allowed portable helpers.

No profile includes FFmpeg, settings, custom `.cube` files, recordings,
snapshots, recovery data, support reports, `.tools`, source-control metadata, or
locally built artifacts not produced by the release job.

Future packaging lives under platform-owned directories such as
`packaging/windows`, `packaging/linux`, and `packaging/macos`, with shared
release identity and payload metadata above them. This is a clean source seam,
not a universal installer. Linux and macOS packages remain absent and
unsupported until each platform has physical acceptance.

## Build and release contract

- GitHub Releases remains the immutable artifact authority. The protected tag
  workflow rebuilds from the verified tagged commit; nobody uploads a local MSI
  or ZIP.
- Go, WiX, signing actions, and packaging dependencies are pinned. The build
  emits the resolved payload list, semantic-to-MSI version mapping, checksums,
  and provenance alongside the artifacts.
- `inlaid.exe` is built once, identity-checked, signed once, and consumed by
  both MSI and ZIP packaging. There is no channel-specific application binary.
- “Reproducible from a verified commit” means the recipe, inputs, payload, and
  identity are fixed and independently inspectable. Trusted timestamp and
  managed-signing responses may prevent byte-for-byte equality of separately
  signed reruns; the project does not claim otherwise.
- Publication remains last: source checks, native CI, vulnerability scan,
  deterministic preview, installer/package tests, allowlist reconciliation,
  signature verification, checksum, and attestation must pass before a release
  is created.
- A WinGet manifest uses the immutable, version-specific GitHub release URL and
  its exact SHA-256. Submission is a separate public pull request and therefore
  needs explicit authorization after the GitHub artifact is independently
  verified.

## Earlier Windows-contract reconciliation

This table is the disposition ledger for the earlier accepted Windows
distribution work. Anything superseded here must be removed from implementation
and tests before the terminal-first contract can be accepted as implemented.

| Earlier guarantee | Disposition in this contract |
|---|---|
| Signed x64 per-user MSI built with pinned WiX | **Retained** as the canonical artifact |
| WinGet points to the same immutable GitHub-release MSI | **Retained**; no second installer build |
| Portable ZIP is secondary and manually updated | **Retained** |
| No in-app updater; Store/MSIX, Chocolatey, and Scoop deferred | **Retained** |
| Normal install needs no elevation; no per-machine variant | **Retained** |
| Start menu shortcut owned by the MSI | **Superseded**; the MSI creates no graphical launcher |
| Installed command exposed through the user `PATH` | **Retained and tightened** with defined equality, append order, persisted provenance, fresh-process refresh, collision, repair, rollback, and fail-safe uninstall rules |
| Installed PowerShell, Command Prompt, and batch wrappers are absent | **Retained** |
| Explorer/Start launches hand off to Windows Terminal | **Superseded**; double-click and Start activation are unsupported and no handoff exists |
| Executable owns Windows Terminal lookup, structured handoff, relaunch marker, and route reporting | **Superseded**; all app-owned routing and route-only evidence are removed |
| Existing terminal and working directory are preserved | **Retained** for every terminal-first invocation |
| Console Host is never an automatic fallback | **Retained by deletion of routing**; it is a characterization target, not a modern-support promise |
| Installed program files are read-only and user data uses known folders | **Retained** |
| FFmpeg is optional, unbundled, and outside live preview and PNG | **Retained** |
| Portable marker and colocated portable layout are authoritative | **Retained** |
| Source mode is explicit and keeps repository-local behavior | **Retained** |
| `--settings` remains a narrow expert/test override | **Retained**; it does not select a distribution mode |
| Portable import is explicit, non-destructive, conflict-reporting, and closed around live recovery tapes | **Retained** |
| Installed, portable, source, and explicit-test layout seam | **Retained** |
| Distribution mode and launch route are separate facts | **Reassigned**; distribution mode remains, while product launch-route detection and reporting are deleted |
| Shared allowlisted payload metadata feeds MSI and ZIP | **Retained**; MSI profile now owns PATH and registration, not a shortcut |
| Stable UpgradeCode, changing product/package identity, and semantic-to-MSI version ledger | **Retained** |
| Upgrade preserves data, blocks downgrade, and rolls back failure | **Retained with a precise boundary**; an incoming failure before `InstallFinalize` rolls back exactly, while later old-product removal occurs after commit and uses visible repair/cleanup recovery rather than an impossible atomic two-product rollback claim |
| Uninstall removes only installer-owned resources and preserves all user data | **Retained and tightened** with persisted provenance and conservative retention of ambiguous or pre-existing PATH text |
| Public EXE and MSI use trusted timestamped signing; ZIP uses the same EXE plus checksum and attestation | **Retained** |
| GitHub Releases and the protected tag build are immutable artifact authority | **Retained** |
| Linux/macOS packaging directories are future seams, not support or publication claims | **Retained** |
| Physical evidence is broader than CI and unavailable Linux/macOS hardware is non-blocking community work | **Retained** |

## Outcome evidence boundaries

### Implemented distribution

Implementation is not accepted from build success alone. It must preserve the
four layout modes and payload seam while removing the shortcut, Explorer
handoff, host lookup, relaunch marker, route reporting, and route-only tests. It
must prove bounded basic-UI and unattended install, repair, persisted user-PATH ownership,
collision behavior, upgrade, blocked downgrade, failed-upgrade rollback,
pre-execution uninstall refusal, successful uninstall after marker finalization,
late-uninstall recovery guidance, uninstall preservation,
non-destructive import, and exact package-derived payload identity. Host refresh
and bare-command discovery are accepted only through the separate physical
terminal-and-shell matrix.
The automated clean-install UI observation is specifically bounded basic UI
(`/qb!`, Windows Installer UI level 3) with its log retained; it does not claim a
human full-UI walkthrough. A distinct `/qn` route proves unattended behavior.

### Terminal-and-shell validation

The finite Windows terminal-and-shell matrix and every required observation are
defined in [TESTING.md](TESTING.md). Results update only the classifications in
[COMPATIBILITY.md](COMPATIBILITY.md). Windows Terminal's existing evidence is a
baseline, not permission to claim other hosts and not a runtime prerequisite.
Full end-to-end evidence belongs to each available terminal-host baseline;
additional shell rows test the shell-specific delta and escalate only when they
expose a reason. Unavailable paid/account-gated hosts and unauthorized WSL rows
are recorded and remain unclaimed; they do not silently block unrelated
publication unless Cody explicitly marks a row mandatory.

### Verified release candidate

Release-candidate preparation owns trusted signing and signature-policy
verification, checksums, attestations, exact payload and executable identity,
WinGet manifest construction and validation, disposable WinGet install/upgrade/
uninstall rehearsal, and a publication rehearsal against immutable candidate
artifacts. These routes remain planned until their implementation and evidence
exist; terminal-and-shell validation does not claim them.

### Publication

Publication owns the separately authorized GitHub release, WinGet submission,
and independent post-publication verification. It consumes an already verified
candidate and never begins merely because terminal-and-shell rows passed.

A validation route may be named as present only while its referenced command or
script exists. Installing paid or account-gated software, enabling WSL or
another optional Windows feature, or using elevation requires separate
authorization. Declined or unavailable optional rows remain unclaimed rather
than becoming false passes or automatic blockers.

The changed terminal-first installation experience requires Cody's subjective
acceptance before publication. This Windows host matrix does not assign Linux or
macOS physical validation to the Windows maintainer and does not create support
claims for those platforms.

## Authorization boundaries

Acceptance of this document settles the durable contract. It does not create a
successor task, authorize implementation, or authorize external mutation.
Neither acceptance nor later local implementation
authorizes acquiring a paid signing service,
creating or changing cloud signing identities, pushing a branch, opening or
merging a pull request, tagging, publishing a GitHub release, submitting to
WinGet, Microsoft Store, Chocolatey, or Scoop, or publishing Linux/macOS claims.
Each consequential external mutation requires its later exact gate.

The parallel “Ongoing: broaden physical evidence” roadmap track remains a
community-validation stream. It is neither assigned to the Windows maintainer
nor a blocker for this distribution work.

[chocolatey]: https://docs.chocolatey.org/en-us/create/create-packages/
[distribution-path]: https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/choose-distribution-path
[known]: https://learn.microsoft.com/en-us/windows/win32/shell/knownfolderid
[msi-arp]: https://learn.microsoft.com/en-us/windows/win32/msi/configuring-add-remove-programs-with-windows-installer
[msi-install-finalize]: https://learn.microsoft.com/en-us/windows/win32/msi/installfinalize-action
[msi-remove-existing]: https://learn.microsoft.com/en-us/windows/win32/msi/removeexistingproducts-action
[msi-environment]: https://learn.microsoft.com/en-us/windows/win32/msi/environment-table
[msi-per-user]: https://learn.microsoft.com/en-us/windows/win32/msi/msiinstallperuser
[msi-version]: https://learn.microsoft.com/en-us/windows/win32/msi/productversion
[msix-alias]: https://learn.microsoft.com/en-us/windows/msix/desktop/desktop-to-uwp-prepare
[signing-options]: https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options
[signtool]: https://learn.microsoft.com/en-us/windows/win32/seccrypto/signtool
[scoop]: https://github.com/ScoopInstaller/Scoop/wiki/App-Manifests
[v020-commit]: https://github.com/Melty1000/inlaid/commit/adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3
[v020-launcher]: https://github.com/Melty1000/inlaid/blob/adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3/START-INLAID.ps1
[v020-packager]: https://github.com/Melty1000/inlaid/blob/adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3/scripts/package-release.ps1
[v020-release-workflow]: https://github.com/Melty1000/inlaid/blob/adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3/.github/workflows/release.yml
[v020-startup]: https://github.com/Melty1000/inlaid/blob/adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3/internal/startup/startup_windows.go
[winget-manifest]: https://learn.microsoft.com/en-us/windows/package-manager/package/manifest
[winget-policies]: https://github.com/microsoft/winget-pkgs/blob/master/doc/Policies.md
[winget-repository]: https://learn.microsoft.com/en-us/windows/package-manager/package/repository
[wix-environment]: https://docs.firegiant.com/wix/schema/wxs/environment/
[wix-major-upgrade]: https://docs.firegiant.com/wix/schema/wxs/majorupgrade/
[wsl-interop]: https://learn.microsoft.com/en-us/windows/wsl/filesystems#run-windows-tools-from-linux
