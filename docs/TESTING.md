# Testing Inlaid

Tests fall into three groups. Keep the result you claim no stronger than the test you ran.

| Evidence | What it proves |
|---|---|
| Cross-build | The shared Go code compiles for a target. It does not compile or exercise a cgo camera bridge. |
| Native CI | Tests, vet, vulnerability scan, and production build pass on that operating system. It does not prove a physical camera or terminal. |
| Hardware report | A named camera, terminal, and build worked together on a physical machine for the recorded checks. |

## Source gate

All platforms require Go 1.26 or newer. Linux native builds also need a C compiler, `pkg-config`, and libturbojpeg 2.0 or newer development files. macOS native builds need Apple Clang and the macOS SDK.

Run from the repository root:

```text
go test ./...
go vet ./...
go build -trimpath ./cmd/inlaid
```

The release gate also runs `govulncheck` and the native jobs in `.github/workflows/ci.yml`. Do not publish a platform build from a cross-build alone.

## Windows distribution routes

Windows implementation evidence uses the following repository routes. A route
may be cited as present only while its referenced command or script exists and
matches the accepted contract. The explicit output path makes the binary used by
the later commands reproducible:

```powershell
go test ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
New-Item -ItemType Directory -Force -Path .\bin | Out-Null
$Phase3Version = 'v0.0.0-phase3'
go build -trimpath -ldflags "-X main.version=$Phase3Version" -o .\bin\inlaid.exe .\cmd\inlaid
$env:DOTNET_ADD_GLOBAL_TOOLS_TO_PATH = '0'
$env:DOTNET_CLI_HOME = (Join-Path (Get-Location) '.tools\dotnet-cli-home')
$env:NUGET_PACKAGES = (Join-Path (Get-Location) '.tools\nuget-packages')
dotnet tool install --tool-path .tools\wix wix --version 5.0.2
& .\.tools\wix\wix.exe --version
./scripts/test-release-package.ps1 -Version $Phase3Version
./scripts/test-windows-msi-package.ps1 -Wix ./.tools/wix/wix.exe -OutputDirectory ./.tools/evidence/windows-msi-package
./scripts/test-windows-msi.ps1 -Wix ./.tools/wix/wix.exe -AcceptInstall
./bin/inlaid.exe --version
./bin/inlaid.exe --render-preview 80x24
```

The versioned build is deliberate: package tests require the executable to
report the exact leading-`v` identity that names the artifact. The pinned-WiX
command is the same setup route used by Windows CI and creates
`.tools\wix`; it needs the .NET SDK and package access. The process-scoped .NET
variables keep CLI state and packages beneath ignored `.tools` and prevent the
SDK from adding its tools directory to persistent user `PATH`. The safe MSI
package route builds real fixtures, runs applicable ICE validation, decompiles
them, and asserts their summary, property, directory, component, registration,
custom-action, and absent Shortcut/Environment tables without installing them.
Only after that evidence passes does the lifecycle route install, repair,
upgrade, and uninstall test packages, and it therefore runs only with its
explicit switch in an authorized disposable Windows environment. Windows CI
does not invoke that mutating script as the elevated runner identity. It invokes
`test-windows-msi-as-standard-user.ps1` with `-AcceptLocalUserSetup` and
`-AcceptMachinePolicyOverride`, which refuses to run outside a GitHub-hosted
Windows runner. The hosted image can set Windows Installer
[`DisableMSI`](https://learn.microsoft.com/en-us/windows/win32/msi/disablemsi)
or
[`DisableUserInstalls`](https://learn.microsoft.com/en-us/windows/win32/msi/disableuserinstalls)
machine policy that rejects unmanaged non-elevated per-user packages before
package sequencing. On the hosted Windows Server image, Windows Installer can
report effective `DisableMSI=1` even when that policy value and key are absent.
The wrapper therefore captures both policy DWORDs, requires the separate
override switch, establishes explicit `DisableMSI=0`, and changes
`DisableUserInstalls` only when a present value is blocking. For the bounded
disposable test window it may atomically create only the exact missing
`Installer` policy key and distinguish that creation from opening a concurrently
created key; opening an existing key fails closed. On every exit it restores
each original value or absence independently and verifies the resulting policy
values. If the wrapper created the key, cleanup also proves the key contains no
values or subkeys. It deliberately does not delete that empty key because an
empty-check followed by registry-key deletion could erase a concurrently added
value; the empty key remains contained to the disposable VM until GitHub destroys
the runner.
That runner normalization is evidence setup, not product behavior or permission
to change a persistent machine. The wrapper then creates a random temporary
local Users-group account, loads that account's profile, proves the child token
lacks the Administrators SID and role, and invokes the lifecycle route. Neither
route is a documentation-only check. The presence or success of an MSI route is
not by itself contract-compliance evidence. Implementation acceptance requires
the MSI authoring and lifecycle assertions invoked by those routes to prove
terminal-first behavior, absence of graphical launchers and handoffs, defined
PATH provenance, and the complete lifecycle contract below.

Roadmap outcome ownership is exact:

| Roadmap phase | Outcome | Evidence owned here |
|---|---|---|
| Phase 1 | Linux contract | Documentation-only Linux contract validation across the five authoritative documents; no package, hardware, or publication evidence |
| Phase 2 | macOS contract | Documentation-only accepted macOS contract validation across the six authoritative documents; no implementation, signing, notarization, hardware result, or publication is implied |
| Phase 3 | Windows implementation | Local automated and disposable-environment evidence for the terminal-first MSI lifecycle, PATH provenance, four layouts, non-destructive import, payload identity, and manual portable-update preservation |
| Phase 4 | Windows terminal-and-shell validation | The finite available host baselines, representative shell deltas, optional interoperability characterizations, defect retests, and compatibility classifications below |
| Phase 5 | Verified Windows release candidate | Trusted signing and signature verification, exact payload and executable identity, checksums, attestations, WinGet manifest validation and disposable lifecycle rehearsal, and publication rehearsal |
| Phase 6 | Windows publication and verification | Separately authorized GitHub release and WinGet submission plus independent post-publication verification |

Phase 5 and Phase 6 routes remain planned until their implementation and
evidence exist. A passing terminal-and-shell matrix does not claim a signed
candidate, a valid WinGet manifest, or publication readiness.

For a documentation-contract change, check tracked and untracked files
separately while the distribution contracts remain untracked:

```powershell
git diff --check -- docs/ROADMAP.md docs/COMPATIBILITY.md docs/TESTING.md
foreach ($contract in @('docs/DISTRIBUTION.md', 'docs/DISTRIBUTION-LINUX.md', 'docs/DISTRIBUTION-MACOS.md')) {
    $contractWhitespace = @(& git diff --no-index --check -- NUL $contract 2>&1)
    $contractStatus = $LASTEXITCODE
    if ($contractStatus -gt 1 -or $contractWhitespace.Count -ne 0) {
        throw "$contract whitespace check failed: $($contractWhitespace -join [Environment]::NewLine)"
    }
}
```

The no-index command normally returns status 1 because a non-empty file differs
from `NUL`; no diagnostics and no status greater than 1 is the passing result.
Also perform a semantic cross-document review of [ROADMAP.md](ROADMAP.md), the
accepted [Windows distribution contract](DISTRIBUTION.md), the accepted
[Linux distribution contract](DISTRIBUTION-LINUX.md),
[accepted macOS distribution contract](DISTRIBUTION-MACOS.md),
[COMPATIBILITY.md](COMPATIBILITY.md), and this file. Trace each durable
decision—not just a keyword—through its owner and consumers. For Windows, trace
native-shell versus WSL commands; artifact/update choices; PATH normalization,
insertion, provenance, repair, rollback, and uninstall; four layouts and payload
profiles; Windows-version and host classifications; available, unavailable,
and escalated matrix rows; phase ownership; and authorization/publication
boundaries. For Linux, trace the Ubuntu 24.04 `amd64` boundary; direct `.deb`
  and GitHub-only channel; package identity `inlaid-terminal-webcam`, its
  Debian-policy documentation, `/usr/bin/inlaid` discovery, and preinst handling
  for foreign ownership, dangling symlinks, and diversions; XDG and user-dir
  resolution; package lifecycle and home-data preservation; V4L2,
  libturbojpeg, FFmpeg, and xdg-utils ownership and Ubuntu security coverage;
  immutable baseline and moving-runner identity; shared payload preservation;
  version mapping; online and offline workflow/ref/digest-constrained
  attestation evidence plus trusted root; same-version immutable-release timing;
  physical community evidence; and every publication and external-action gate.
For macOS, trace the Apple-silicon `arm64` and explicit 15.0 deployment boundary;
the signed, notarized, and stapled self-contained flat `.pkg`; normal Finder and
direct absolute `/usr/sbin/installer` routes using the original package;
GitHub-only discovery; `/usr/local/bin/inlaid` without shell edits; the signed
package-internal guard and non-following checks for every payload parent and
leaf; a signed installed uninstall-helper trust root plus receipt, immutable
manifest/ledger, metadata, digest, and Mach-O signing ownership; fail-closed
repair/update; optional unprivileged preservation copies followed by distinct
exact-leaf administrator remediation for protected collisions; the fixed
manifest parent-creation allowlist and nearest-safe-ancestor pre/post checks;
offline
intact-install uninstall after the download directory is deleted; Apple
Installer-only receipt creation/update and ordered receipt forgetting/final
cleanup; unconditional parent and user-data preservation; optional expert or
damaged-install release evidence that never becomes a normal-lifecycle input;
the publication-required eight-leaf evidence set whose checksum file names only
the other seven leaves; macOS-authenticated package-embedded trust root/policy/
verifier copies that bootstrap byte-identical external copies; stable
non-self-referential policy, verifier identity/version, computed final-package
digest equality with the package as sole attestation subject, and manifest
authentication through byte-identical package embedding plus its checksum;
original-package quarantine preservation and the real Finder/Gatekeeper/Installer
decision, without copied-xattr equivalence; absolute Apple security-tool paths
and PATH-shadow refusal; Foundation Application Support, Caches, Movies,
Pictures, and Documents paths; source migration and absence of a portable
artifact; route-specific Intel classification; cgo, AVFoundation, embedded
privacy text, hardened runtime, camera entitlement, TCC and sandbox boundaries;
system-only runtime libraries, optional FFmpeg; the live PATH-resolved `open`
adapter versus the planned absolute `/usr/bin/open` implementation; shared seam
preservation; version mapping; Developer ID, notarization, Gatekeeper, checksums,
online/offline provenance, same-version immutable-release timing; physical
community evidence; and every publication and external-action gate.

## Linux distribution routes

The source and native-CI commands below exist now. Their recorded Linux evidence
is pinned to portability review commit
`adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3` and native CI run
[`32782751639`](https://github.com/Melty1000/inlaid/actions/runs/32782751639):

```text
go test ./...
go vet ./...
go build -trimpath ./cmd/inlaid
```

The Ubuntu 24.04 CI job additionally installs its native build dependencies,
runs selected race checks and `govulncheck`, and builds `bin/inlaid`. Those
results do not prove an installed XDG layout, `.deb` metadata or lifecycle,
artifact signing/provenance, terminal presentation, or a physical camera.

No Linux package builder, package lifecycle script, or release route exists in
the repository. Do not name one as present. Before Linux publication, planned
repository-owned validation must prove every item in the
[Linux distribution contract's implementation evidence](DISTRIBUTION-LINUX.md#planned-implementation-evidence),
including exact package payload/dependencies, install and removal ownership,
upgrade/downgrade/failure recovery, command collisions, XDG paths and
permissions, migration, optional dependency behavior, Windows seam
preservation, checksums, and online/offline attestation verification.

The package evidence and physical evidence are separate. A clean Ubuntu package
lifecycle does not prove V4L2 hardware or terminal presentation; the physical
route below does not prove package ownership or release provenance.

## macOS distribution routes

The source and native-CI commands below exist now. Their recorded macOS evidence
is pinned to portability review commit
`adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3` and native CI run
[`32782751639`](https://github.com/Melty1000/inlaid/actions/runs/32782751639):

```text
go test ./...
go vet ./...
go build -trimpath ./cmd/inlaid
```

The current `macos-15` arm64 CI job additionally runs `govulncheck`, builds
`bin/inlaid`, extracts its embedded `__TEXT,__info_plist`, and checks the exact
camera purpose string. Those results do not prove an explicit 15.0 deployment
target, signed or installed identity, entitlements, a `.pkg` payload or
lifecycle, Foundation user-data layout, notarization or stapling, Gatekeeper or
quarantine, TCC attribution, terminal presentation, or a physical camera.

No macOS package builder, signing/notarization workflow, package lifecycle
harness, or macOS release route exists in the repository. Do not name one as
present. Before macOS publication, planned repository-owned validation must
prove every item in the [macOS contract's implementation and candidate
evidence](DISTRIBUTION-MACOS.md#planned-implementation-and-candidate-evidence),
including architecture/deployment rejection; the exact payload, installed
manifest/ledger, receipt and signed-helper embedded trust data; and both normal
installation routes. Opening the original package in Finder/Installer and direct
`/usr/sbin/installer -pkg ORIGINAL.pkg -target /` must succeed without an
auxiliary lifecycle executable, copied or expanded package, staged evidence copy,
interprocess authorization, or release-evidence argument. Prove the original
package is the only Installer input and that no lifecycle component downloads
anything.

Exercise clean install, same-version repair, update, injected partial failure and
retry, blocked downgrade, uninstall-then-older-package rollback, damaged-state
repair, and second install. The signed package-internal guard may execute only
from Apple Installer's protected extraction. It must independently authenticate
its embedded target manifest, own Team ID/identifier/signature, architecture,
deployment target, receipt state, every traversed parent, and every payload leaf
before mutation. It accepts no external decision. Test absent, exact, modified,
foreign, symlinked, wrong-type, wrong-owner, wrong-mode, wrong-digest, wrong-Team,
wrong-identifier, and wrong-designated-requirement leaves, plus parent symlink and
race cases. Exact current leaves may be replaced and missing leaves restored;
every other collision fails closed. All parent directories survive.
Starting with only `/` and `/usr`, then with each possible nearest existing safe
ancestor, prove the guard may create exactly `/usr/local`, `/usr/local/bin`,
`/usr/local/share`, `/usr/local/share/doc`, `/usr/local/share/doc/inlaid`,
`/usr/local/share/inlaid`, `/usr/local/libexec`, and
`/usr/local/libexec/inlaid`. Each child must be exclusively created beneath the
reverified parent and immediately reverified as a non-symlink `root:wheel` mode
`0755` directory with no non-root write before descent. Test every pre-existing
safe parent, missing allowlisted chain, path outside the allowlist, unexpected
missing component, foreign object, symlink, wrong type/owner/mode/ACL, concurrent
creation/substitution, and pre/post-check failure. No parent is removed on
uninstall.
Prove in-place updates keep the accepted destination-role set stable, add only
target-manifest absent leaves, and never delete an obsolete path; a release that
must retire a path is rejected until uninstall/clean-install or a separately
accepted contract governs it.

For modified leaves, prove repair, update, and recovery refuse before overwrite
and never write a privileged backup to a home or user-selected path. End the
privileged lifecycle attempt after it reports one exact manifest leaf and its
classification. If readable, exercise an optional preservation copy to a
user-owned destination while unprivileged; prove that step cannot unlink,
rename, replace, or authorize the protected leaf. Separately exercise an
informed administrator remediation that names only that reported exact leaf
under freshly reverified safe parents. Reject roots, parent directories,
wildcards, globs, recursive operations, extra paths, leaf/parent following,
automatic adoption, and stale-report authority. A later lifecycle invocation
must inventory afresh and proceed only when the leaf is absent or exact. Cover
repair, update, failed-update recovery, rollback after a retained uninstall
collision, damaged recovery, and reinstall. No prior diagnostic, receipt, BOM,
or checksum alone authorizes adoption or deletion.

Delete the package download and all eight external evidence leaves, then prove
the intact installed command still runs and
`/usr/bin/sudo /usr/local/libexec/inlaid/uninstall` completes offline. Before
deletion, the helper must authenticate its own root-owned single-link regular
Mach-O type/mode/signature/Team ID/identifier/designated requirement, use its
signed release policy and allowlist plus its computed self-digest to authenticate
the manifest, then derive and authenticate the canonical immutable ledger;
reconcile the receipt through `/usr/sbin/pkgutil`; and
inventory every leaf non-followingly. It deletes only exact genuine leaves,
retains and reports modified/foreign/ambiguous paths, preserves all parents and
user data, forgets the receipt only after other genuine leaves are gone, and
removes ledger, manifest, and helper in the defined final order. Inject failure
before and after every deletion, at receipt forgetting, and at each final-cleanup
step; prove a retry never loses the trust material still needed for its next
step or broadens the allowlist. Exercise all three valid post-receipt states:
exact ledger plus exact manifest plus helper; exact manifest plus helper after
ledger removal; and helper alone after both metadata leaves are gone. In each
state, revalidate the manifest from signed stable expectations plus the computed
helper self-digest when it remains, then compute its digest before deriving any
remaining ledger. Explicitly reject manifest-absent plus ledger-present, as well
as modified or wrong-type metadata, and remove only authenticated ledger, then
manifest, then helper.

Damaged-install recovery uses an exact current or target package through the
same Finder/direct-Installer route. Test missing/corrupt application, helper,
manifest, ledger, and receipt; mixed exact-current/exact-target state after a
failed update; and a third-value collision. Only Apple Installer creates or
updates a receipt. Optional external release evidence may verify or diagnose but
must not add mutation authority or be required by ordinary install, repair,
update, or intact uninstall. The immutable ledger never carries retry state and
no persistent operation journal is authoritative.

Retain the eight publication-evidence leaves and prove that `SHA256SUMS.txt`
hashes the other seven and not itself; macOS independently authenticates the
final package before its non-executable embedded trusted-root/policy/verifier
copies bootstrap byte-identical external copies; the policy is stable and
contains no final-package digest; verifier
identity/version is fixed; the verifier-computed final-package digest equals the
sole attestation subject; and the external manifest authenticates through
byte-identical package embedding plus its checksum. Verify Team ID, identifiers,
designated requirements, entitlements, Developer ID Installer signature,
notarization, stapling, and final installed signatures. Use hostile PATH shadows
for `sudo`, `shasum`, `codesign`, `pkgutil`, `spctl`, `installer`, and every
security helper, and prove the contract's absolute system paths decide trust,
receipt state, and lifecycle behavior. Also prove Foundation paths, migration,
AVFoundation/TCC boundaries, system-library closure, optional FFmpeg, and
accepted Windows/Linux seam preservation.

For ordinary package inputs, record whether `com.apple.quarantine` is absent or
present on the original file and record the actual Gatekeeper/Installer result.
Prove Finder and direct `/usr/sbin/installer` receive that original file: no
Inlaid lifecycle component copies the package, copies raw xattr bytes, removes,
rewrites, synthesizes, or weakens the attribute, or claims a new inode preserves
Apple provenance/approval semantics. Package SHA-256 and attestation identity
remain byte identities independent of quarantine metadata.

For the browser-download physical case, require quarantine present on the exact
original package before Finder opens it. Record privacy-safe presence, length,
and value SHA-256 plus the real first-open/Gatekeeper/Installer decision and
prove the original attribute is unchanged by every Inlaid lifecycle route. Test
both absent and present ordinary inputs, concurrent replacement of the original
path before invocation, user cancellation, assessment failure, and Installer
failure. These cases must fail or report the real OS result without invoking
custom pre-Installer machinery, clearing quarantine, or mutating payload outside
Apple Installer's transaction.

The current Darwin Open Folder route is exactly
`exec.CommandContext(ctx, "open", path)`, so current source evidence resolves
`open` through `PATH`. The macOS contract deliberately plans absolute
`/usr/bin/open` for installed implementation. Candidate validation must prove
the code change in source, installed, and explicit-test modes, demonstrate that
a fake earlier `open` on `PATH` is never invoked, and preserve the output plus
its displayed path when the absolute helper fails.

Package, cryptographic, and physical evidence remain separate. A clean package
lifecycle and accepted notarization do not prove camera permission, hardware,
or terminal presentation; the physical route below does not prove package
ownership, immutable provenance, or release readiness.

## Physical-camera checks

Ordinary tests never open a camera. The following checks are opt-in.

### Windows

```powershell
$env:INLAID_MF_CAPTURE_REAL = '1'
go test .\internal\capture .\internal\cellreduce

$env:INLAID_LIVE_TEST = '1'
$env:INLAID_TEST_DEVICE = 'Camera name shown by Inlaid'
go test -v .\internal\dashboard -run 'TestRuntimeLiveCameraRecordAndSnapshot|TestBubbleTeaProgramReceivesLiveCameraPreview'

$env:INLAID_LIVE_SOAK = '1'
go test -timeout 15m -v .\internal\dashboard -run '^TestRuntimeLiveCameraRecordAndSnapshot$'
```

Some Media Foundation lighting checks are specific to the Logitech C922. The optional three-minute capture soak also requires `INLAID_MF_CAPTURE_SOAK=1`; it is not part of routine development. The dashboard soak records for ten minutes, measures retained heap and queue pressure, and decodes the resulting MP4, GIF, and PNG. Its test timeout must exceed the recording duration; the documented command allows 15 minutes.

For the currently published ZIP, follow that release's launcher instructions and record the launch method. For the terminal-first distribution candidate in native Windows shells, close every instance of the host, open a fresh host process and chosen shell, and run `inlaid` through the user `PATH`. WSL interoperability rows use `inlaid.exe` exactly as specified below. Explorer double-click, Start-menu activation, and application-owned Windows Terminal handoff are not acceptance routes. Console Host is run only for the characterization rows below.

### Windows host-and-shell validation (roadmap Phase 4)

This is the finite pre-publication coverage matrix for the terminal-first
Windows distribution. It holds PowerShell 7 constant across modern terminal
hosts, uses Git Bash's native mintty pairing, then tests shell-family deltas in
Windows Terminal and VS Code instead of repeating the full Cartesian product.

| ID | Terminal host | Shell and command | Evidence tier |
|---|---|---|---|
| H01 | Windows Terminal | PowerShell 7; `inlaid` | Full host baseline |
| H02 | VS Code integrated terminal | PowerShell 7; `inlaid` | Full host baseline |
| H03 | WezTerm | PowerShell 7; `inlaid` | Full host baseline |
| H04 | Alacritty | PowerShell 7; `inlaid` | Full host baseline |
| H05 | Tabby | PowerShell 7; `inlaid` | Full host baseline |
| H06 | Warp | PowerShell 7; `inlaid` | Availability-gated full host baseline; paid/account authorization may be declined |
| H07 | Hyper | PowerShell 7; `inlaid` | Full host baseline |
| H08 | Cmder using its bundled ConEmu host | PowerShell 7; `inlaid` | Full host baseline for this exact product configuration |
| H09 | Standalone ConEmu | PowerShell 7; `inlaid` | Separate full host baseline; H08 does not cover it |
| H10 | Git Bash's mintty | Git Bash; `inlaid` | Full native-pairing host baseline |
| S01 | Windows Terminal | Windows PowerShell 5.1; `inlaid` | Shell delta |
| S02 | Windows Terminal | cmd; `inlaid` | Shell delta |
| S03 | Windows Terminal | Git Bash; `inlaid` | Shell delta |
| S04 | Windows Terminal | Nushell; `inlaid` | Shell delta |
| S05 | VS Code integrated terminal | Windows PowerShell 5.1; `inlaid` | Shell delta |
| S06 | VS Code integrated terminal | cmd; `inlaid` | Shell delta |
| S07 | VS Code integrated terminal | Git Bash; `inlaid` | Shell delta |
| S08 | VS Code integrated terminal | Nushell; `inlaid` | Shell delta |
| X01 | Windows Terminal hosting WSL Bash | WSL Bash; `inlaid.exe` | Optional Windows-executable interoperability characterization |
| X02 | VS Code integrated terminal hosting WSL Bash | WSL Bash; `inlaid.exe` | Optional Windows-executable interoperability characterization |
| C01 | Windows Console Host | Windows PowerShell 5.1; `inlaid` | Console characterization only; no modern-support promise |
| C02 | Windows Console Host | cmd; `inlaid` | Console characterization only; no modern-support promise |

Every row records one of `passed`, `failed`, `classified-limitation`,
`unavailable-not-authorized`, or `unavailable-not-present`, with the reason and
date. `H06`, `X01`, and `X02` are availability-gated: declining an account/paid
gate or WSL enablement records
the row as unavailable and leaves it unclaimed without blocking unrelated
publication unless Cody explicitly designates that row mandatory. Never install
paid/account-gated software, enable WSL or another optional Windows feature, or
use elevation without separate authorization. Other required rows are not
silently omitted.

#### Common evidence for every executed row

1. Record exact Windows edition/build/architecture, Inlaid semantic version and
   candidate SHA-256, terminal host/version, shell/version, matrix ID, and any
   non-default host setting affecting color, glyphs, mouse, PATH, or shell
   integration. Remove usernames and private paths.
2. Use the same candidate MSI. Close every host instance, start a fresh host
   process, and record command discovery. Native Windows-shell rows record
   `where.exe inlaid` plus the shell-native result and require bare `inlaid` to
   resolve to the candidate. WSL rows record `command -v inlaid.exe`, confirm
   Windows-executable interoperability and Windows PATH import, and invoke only
   `inlaid.exe`; they never count as bare-`inlaid` evidence.
3. Start outside the install directory, record the working directory before and
   after the run, and prove it is preserved. Confirm installed settings and
   outputs use known folders instead of the working directory.
4. Run the row's documented command with `--version` and require the expected
   semantic version. Run its `--render-preview 80x24` twice and require
   successful, byte-identical output containing the expected Inlaid page.
5. Launch and exit interactive Inlaid, require clean terminal restoration, and
   exercise one useful shell/launch failure such as a PATH collision or malformed
   argument. Require bounded actionable text, no handoff to another terminal, no
   private-path disclosure, and a clean exit.

#### Full evidence for each host baseline

Every `H` row adds the full end-to-end suite; evidence from one host never
substitutes for another:

- record the terminal grid and scrubbed screenshot or recording; verify 24-bit
  foreground/background color, block and quadrant glyphs, stable layout,
  readable focus/status states, and absence of mojibake or stale content;
- exercise complete keyboard navigation and activation plus mouse hover, focus,
  and activation when supported, otherwise prove keyboard completeness and
  record the host limitation;
- resize above and below 80x24 and back, requiring the bounded recovery message,
  correct redraw, no panic, and no permanently corrupted terminal state;
- open the named physical camera, record its actual mode and sustained preview,
  save and inspect a PNG plus at least one short MP4 or GIF, and reconcile their
  colors with the terminal's canonical cell representation;
- exit once with a documented quit key and once with `Ctrl+C`, requiring clean
  restoration, completed or recoverable recording, camera release, and immediate
  reopen by Inlaid or another camera application; and
- exercise a host-relevant failure such as below-minimum size, camera ownership,
  camera absence, or missing FFmpeg, requiring useful bounded behavior and camera
  release. A baseline without the required media route remains incomplete.

#### Shell deltas and characterization rows

Each `S` row runs only the common evidence because its terminal host already has
a full baseline. Any shell-specific visual, input, resize, camera, media, exit,
or failure difference escalates that row to the full host suite before it can
pass.

Each `X` row runs the common evidence with `inlaid.exe` and records WSL version,
distribution, interop state, PATH import, and Windows/Linux working-directory
behavior. It is an interoperability characterization, not a native Linux build
or bare-`inlaid` support claim. Any interactive difference escalates the row to
the relevant full checks.

Each `C` row runs the common evidence plus color, glyph, keyboard, mouse where
available, resize, terminal restoration, and useful-failure characterization.
Camera/media repetition is not required because Console Host is not a modern
support target; an unexpected result may trigger a separately classified full
run.

For MSI lifecycle evidence, separately record a bounded Windows Installer basic-UI
install (`/qb!`, UI level 3 with cancellation disabled for deterministic CI) and
an unattended `/qn` install, repair, upgrade from an older test MSI, blocked downgrade, injected
pre-finalize failed-upgrade rollback, successful post-finalize old-product removal, safe
pre-execution uninstall refusal, and successful uninstall after marker
finalization. Fault-injected repair uses `/i` with `REINSTALL=ALL` and
`REINSTALLMODE=a`, not `/f`, because [Windows Installer's `/f` option ignores
command-line property values](https://learn.microsoft.com/en-us/windows/win32/msi/command-line-options);
the retained log must prove the exact injected property reached the server
session. Reconcile program files,
Windows Installer registration, the persisted PATH provenance marker, literal
and normalized PATH state, exact `REG_SZ`/`REG_EXPAND_SZ` value kind, and rollback
state, including the exact raw registry bytes at every supported failed-transaction rollback boundary. Exercise both an originally absent
user PATH and an originally present empty PATH transactionally; prove failed
install restores the first as absent and that successful uninstall preserves
the original absence or present-empty state. Attempt legal zero-byte,
unterminated, single-NUL, and multiply NUL-terminated fixtures before any MSI
transaction, retaining requested and observed type/bytes. When Windows preserves
the exact representation, run the full lifecycle and require failed install to
restore its exact type and bytes. When `RegSetValueExW` canonicalizes an unusual
representation during fixture setup, record that host behavior, do not claim
live MSI coverage for that representation, and retain the exact-byte helper unit
coverage instead. Reject odd byte counts, unpaired UTF-16 surrogates, and
embedded NUL followed by content. After a successful install and
uninstall, preserve presence, registry type,
and decoded segment text or emptiness; the helper may conservatively serialize that
value with exactly one trailing UTF-16 NUL because committed provenance does not
retain a whole-PATH byte backup. The already canonical single-NUL fixture remains
byte-identical. A malformed raw PATH must make
`ApplyUserPath` fail before PATH, provenance, or snapshot mutation. Windows
Installer then rolls back earlier transient package work, and rollback consumes
the preflight claim; final evidence requires exact original PATH type/bytes and
zero package, registration, known-marker, snapshot, `.partial`, `.claim`, or
`.claim.partial` residue.
This does not claim that no transient package file or preflight claim existed.
Repair/reacquisition
evidence must prove the marker preserves or refreshes that prior-presence field
at the exact ownership boundary. Major-upgrade authoring must schedule
`RemoveExistingProducts` after `InstallFinalize` and must not ignore an
old-product removal failure. The retained successful-upgrade log must prove the
incoming transaction finalized before old-product removal began, then prove the
new product is exact and the old ProductCode is absent.
The lifecycle preflight must also prove both fixture ProductCode-qualified
snapshot, `.partial`, `.claim`, and `.claim.partial` paths are absent. Static/package evidence must
prove the same-product stale-state check executes immediately before rollback is
scheduled, `NOT UPGRADINGPRODUCTCODE` appears directly on every PATH transaction
action, and `NOT RollbackDisabled` is present in the LaunchCondition table.
Live evidence must create one exact, harness-owned stale transaction fixture and
prove `PreflightUserPathState` refuses uninstall before the first
`InstallExecute` or `InstallFinalize` script. Product registration, payload,
marker, PATH bytes/type, and the fixture itself must remain exact until the
harness removes only that fixture. Do not deliberately inject a deferred failure
after complete-removal execution begins, including inside the nested old-product
uninstall during a major upgrade. Windows Installer automatically adds
product-registration removal to that script, and a standard-user package cannot
promise exact registration rollback across that platform boundary. Because the
incoming package commits first, an old-product removal failure must leave the
newer product available for repair/retry; evidence must report any older
registration residue rather than claiming atomic two-product rollback.
Static inspection must also compare the complete seven-row Registry table—root,
key, name, encoded value, owning component, and component key-path reference—to
the expected mapping; checking only the provenance row is insufficient.
It must also prove uninstall consumes the known marker values before
`RemoveRegistryValues`, then runs a normal deferred finalizer after that standard
action and before commit. Empty `Components` and installer-private marker keys
are retained because registry key deletion cannot atomically guarantee they are
still empty; a foreign value or subkey racing in after inspection must never be
deleted. Successful uninstall evidence accepts an absent key or only those empty
structural keys, requires every known marker/component value and transaction file
absent, and fails on any unexpected value or subkey. Harness cleanup must not
recursively delete this registry structure or otherwise consume the unexpected
state. The authenticated claim progresses from non-mutating `preflight`, to
`active` only after the snapshot is durably published, and finally to `cleanup`
before either transaction file is deleted. Rollback must remove a preflight
claim when validation failed before snapshot publication and may idempotently
restore any valid authenticated snapshot; it must never treat active-without-
snapshot as restored. Inject failure at activation, cleanup transition, and each
snapshot/claim deletion, then restart: active state must re-run rollback
verification, while a later serialized preflight must finish cleanup-only
residue before exclusively creating a fresh preflight claim. It must never
consume active state, and stale final or partial snapshots must remain untouched.
Failure injection may be enabled only when the
builder is explicitly invoked as a test build; production-shaped builds must
reject that combination. Package evidence must build with hostile ambient
`GOFLAGS` in both directions, prove the builder restores that environment, and
runtime-probe both the built and MSI-exported helper: production mode exits
successfully and silently, while only `-TestBuild -EnableTestHooks` emits the
exact deliberate-failure line and exit code.
The disposable PowerShell lifecycle harness does not claim that an ordinary
new terminal host inherited an environment-change broadcast. CI constructs an
explicit nonsecret child tool environment for the temporary user, and the
lifecycle synthesizes effective `PATH` where a test requires it; neither is
fresh-host evidence. Phase 4 must therefore close every host instance and open a
genuinely new host and native shell after each PATH change. The automated harness
proves only persisted PATH bytes/type, marker/transaction behavior, and direct
execution of the installed executable.
The rollback-disabled case passes public `DISABLEROLLBACK=1` and retains the MSI
log proving the package's LaunchConditions action emitted the rollback-required
message and failed before mutation. The successful-upgrade log must prove
`InstallFinalize` returned success before `RemoveExistingProducts` started and
that old-product removal then succeeded. The pre-execution uninstall-refusal log must prove the stale-state
preflight failed before `InstallExecute`, `InstallFinalize`, product-unregister,
product-unpublish, or source-list-unpublish activity. A successful uninstall log
must prove `FinalizeUserPathMarker` ran successfully and `InstallFinalize`
completed. Successful lifecycle evidence retains those logs, complete recursive
registration key/value-kind/raw-value inventories with no volatile exclusions,
cached-MSI hashes, and exact path/length/SHA-256 inventories for every user-data
fixture.
The lifecycle script must acquire raw PATH evidence through bounded native
`RegQueryValueExW` calls and restore fixtures through `RegSetValueExW`; decoded
.NET strings may support semantic PATH checks but are not raw-byte evidence.
The basic-UI route is automation evidence for Windows Installer UI level 3, not
a claim that a human completed a full installer wizard walkthrough. Its MSI log
must contain the exact UI level, while separate `/qn` evidence remains mandatory.
On both success and failure, CI copies bounded logs, snapshots, inspected package
evidence, fixture packages, process records, and inventories under `.tools/evidence`,
then uploads the package, lifecycle, standard-user-wrapper, and focused-helper
roots with an always-run pinned artifact action. The standard-user wrapper grants
the temporary SID read/execute access to the source tree, explicitly denies that
SID source writes/deletes/ACL ownership changes despite broader inherited runner
ACLs, and grants Modify only on protected `windows-msi-standard-user` and
`windows-msi-lifecycle` evidence roots. Before the lifecycle starts, child and
parent evidence must agree on effective write-denial probes for source, package,
and focused-helper state and successful create/write/delete probes only in those
two owned evidence roots. Orchestrator evidence must record the original and
effective `DisableMSI` and `DisableUserInstalls` state; cleanup evidence must
record the final state, and success requires exact restoration. The wrapper then
launches a hidden
credentialed child with a loaded HKCU profile,
and places its process tree in a kill-on-close job with a 40-minute outer timeout.
It records the actual child token, group SIDs, `whoami /all`, resolved profile,
allowlisted runner facts, exact child exit, and safe DACL restoration. Each of
the three restored directory DACLs must retain byte-identical ordered ACEs and
all original control flags; the only accepted normalization is Windows adding
`DiscretionaryAclAutoInherited` when `Set-Acl` reapplies the captured descriptor.
Cleanup evidence records exact restoration versus that one-way normalization,
and any other permission, ordering, inheritance, protection, or control-flag
change fails closed. Parent
success additionally requires the retained token and completion JSON to prove the
expected SID, a non-administrator token, a passing lifecycle, and no failure.
The lifecycle establishes its evidence destination and top-level failure boundary
before known-folder, native-helper, or registry initialization. Its temporary
directory and retained run directory contain spaces so the real Windows Installer
client route exercises quoting. Every client call has a six-minute timeout beneath
the job timeout and records its exact PID, log state, and outcome. On timeout it
requests termination only of that exact client, marks Windows Installer service
completion unknown, retains evidence immediately, and suppresses lifecycle cleanup
mutations rather than claiming service quiescence.
For any unexpected failure, a live pre-cleanup snapshot reaches the retained
evidence root before cleanup uninstall, PATH restoration, or exact owned-fixture
deletion; a separate post-cleanup or cleanup-suppressed snapshot records what
happened afterward. Pre-cleanup runner evidence records the Windows/runner image,
PowerShell, .NET, WiX, process identity, current-token administrator role, and
`whoami /all`. The wrapper's separate child evidence is what proves that the live
lifecycle ran under the expected non-administrator SID; runner-admin evidence
alone still does not prove a standard-user run. If the inner lifecycle fails
after invocation, reports an MSI timeout/service-unknown state, or the outer
child/evidence boundary is uncertain, the wrapper terminates its contained
process tree but intentionally does not remove the temporary account, profile,
HKCU probe, or temporary SID ACLs. Its cleanup JSON records that preservation,
and the disposable VM—not a racing cleanup action—becomes the final containment
boundary.
The GitHub Actions artifact is retained for 14 days; Main must download or
otherwise archive the required run before expiry and record the run/artifact
identity used for acceptance. This route proves per-user HKCU and MSI behavior
for the temporary standard-user token on the recorded hosted image. It does not
prove a physical terminal/camera, a fresh interactive terminal environment
broadcast, or behavior on a persistent/self-hosted machine, where the wrapper
refuses to run.

Focused `go test -v -json` evidence covers injected snapshot publication,
claim activation, cleanup-deletion, and restart failures in the PATH helper.
Static decompiled-MSI evidence independently proves action types, conditions, and
sequencing. The live MSI harness independently proves ordinary and public
post-action failure lifecycle behavior. Acceptance composes all three evidence
classes; it does not claim that helper file-deletion faults were injected through
`msiexec` unless a future live route actually does so.
Inject a resolved program-directory value containing `;` and prove install,
repair, and upgrade each fail closed without committing PATH or provenance state.
Prove every settings, recovery, recordings, snapshots, filters, and
support-report location survives repair, rollback, and uninstall.

For Phase 3 portable-import evidence, use both a current marker-bearing fixture
and a markerless fixture shaped from the pinned published `v0.2.0-beta.1` ZIP.
Prove the latter contains the baseline's root launchers and `bin\inlaid.exe`,
the complete pinned release-owned documentation/helper shape, and no portable
marker, and is accepted without executing any release-owned file. Prove an
arbitrary markerless folder, a source tree, a current root-level
executable without its marker, and a legacy/current hybrid are each refused
before settings or filters are written. Both accepted routes remain explicit,
copy only settings and top-level `.cube` filters without overwrite, retain media
and support reports in place, and refuse live recovery tapes.

For Phase 3 portable-update evidence, populate an older portable root with its
marker/versioned manifest, settings, recovery data, recordings, snapshots,
filters, support reports, and optional `.tools`; record exact path, length, and
SHA-256 evidence; then apply the documented manual update. Prove the new
executable and every new release-owned payload file match the new manifest, any
removed file was proven release-owned by the prior manifest, and every
user-writable item remains byte-identical. Prove marker authority and presence
are retained while `inlaid-portable.json` atomically advances to the exact new
versioned manifest. Also prove the documented procedure never deletes or replaces
the entire portable root and that interruption after both replacement and
obsolete-file removal is recoverable. A fully self-consistent malicious marker,
state, and backup that claims an ordinary user file must be rejected by the
fixed role-to-destination contract before any portable-root mutation.
An incomplete target manifest, including a target containing only a known
retired prior-manifest pair, must likewise fail before transaction state or any
portable-root mutation.

Each defect is fixed and the affected rows rerun, or explicitly classified with
scope and rationale. Unavailable rows remain unclaimed rather than becoming
passes. Compatibility claims must match the completed evidence exactly.

### Linux

```text
INLAID_V4L2_CAPTURE_REAL=1 go test -v ./internal/capture
INLAID_LIVE_TEST=1 INLAID_TEST_DEVICE='Camera name shown by Inlaid' go test -v ./internal/dashboard -run 'TestRuntimeLiveCameraRecordAndSnapshot|TestBubbleTeaProgramReceivesLiveCameraPreview'
```

`INLAID_V4L2_CAPTURE_DEVICE` may select an exact device ID or displayed name. If it is unset, the capture test uses the first enumerated camera. Do not publish a stable device path, USB serial, host name, or username in the report.

The V4L2 capture command is the existing native Linux physical-camera route. The
dashboard command is narrower and is **not a ready Linux acceptance route**: its
live recording test overrides `INLAID_FFMPEG` internally and requires an
executable at the source tree's exact `.tools/ffmpeg/bin/ffmpeg` path. The only
repository provisioner for that path is Windows-only. Installing distro FFmpeg,
putting `ffmpeg` on `PATH`, or setting `INLAID_FFMPEG` does not satisfy that test
as written. Until a later implementation supplies and validates a Linux route,
the command may be recorded only when that exact prerequisite was supplied
separately; it cannot prove installed-package FFmpeg discovery, and its absence
does not invalidate the V4L2 source classification.

### macOS

```text
INLAID_AVF_CAPTURE_REAL=1 go test -v ./internal/capture
INLAID_LIVE_TEST=1 INLAID_TEST_DEVICE='Camera name shown by Inlaid' go test -v ./internal/dashboard -run 'TestRuntimeLiveCameraRecordAndSnapshot|TestBubbleTeaProgramReceivesLiveCameraPreview'
```

Grant camera access to the terminal or executable running the test. Record whether permission was first granted, already granted, or denied and recovered; do not publish the AVFoundation unique ID.

The AVFoundation capture command is the existing native macOS physical-camera
route. The dashboard command is **not a ready macOS acceptance route**: its live
recording test overrides `INLAID_FFMPEG` internally and requires an executable at
the source tree's exact `.tools/ffmpeg/bin/ffmpeg` path, while the repository has
no macOS provisioner for that prerequisite. A user-installed FFmpeg on `PATH` or
an external `INLAID_FFMPEG` value does not satisfy the test as written. Until
later implementation supplies and validates a macOS route, the command may be
recorded only when that exact prerequisite was supplied separately and cannot
prove installed-package FFmpeg discovery.

Installed-candidate physical evidence additionally uses the exact final stapled
package obtained through a quarantining browser-download route with Gatekeeper
enabled. It requires `com.apple.quarantine` present on that original package,
opens the original directly in Finder/Installer, and records privacy-safe
presence, length, value SHA-256, the real first-open/Gatekeeper/Installer
decision, and proof no Inlaid lifecycle route changed the attribute. Raw
attribute bytes remain private and are not package SHA-256 or attestation
identity. Exercise the same original package through the direct absolute
`/usr/sbin/installer` route as a separate observation. Record package and
executable identity, receipt, fresh-shell command resolution, actual TCC subject,
first grant, remembered grant, denial and user-driven recovery, repair, update,
offline uninstall after deleting downloaded/evidence files, reinstall,
camera/media/recovery, terminal restoration, and offline behavior. Never remove,
replace, copy, or synthesize quarantine, weaken Gatekeeper, reset TCC, install
software, change privacy settings, or authorize an administrator operation
without the separate authority for that validation.

Unset the opt-in variables after testing so a later test run cannot open the camera unexpectedly.

## Acceptance run

Use a physical machine and keep the application open long enough to expose cadence or retained-memory problems. Ten minutes is a useful baseline; longer reports are welcome.

Record:

- release tag or exact commit and how it was obtained;
- OS or distribution version and architecture;
- general machine class without serial numbers or account names;
- camera model and the actual mode selected by Inlaid;
- terminal and version, shell and version, and launch method;
- terminal grid, sustained source and shown FPS, skipped frames, and test duration;
- whether retained memory stabilized or continued growing;
- PNG, MP4, and GIF results, including recording duration;
- camera close, reopen, removal, permission-denied, and another-application ownership behavior when exercised.

Generate, inspect, and scrub the evidence locally first. Opening the public
[compatibility report form](https://github.com/Melty1000/inlaid/issues/new?template=02-compatibility-report.yml),
submitting an issue, or uploading attachments is a separate external mutation
and requires explicit authorization. If submission is authorized, a failure is
useful evidence; describe it instead of marking the entire platform unsupported.

## Privacy

Issue text and attachments in this repository are public. Remove camera images, recordings, absolute paths, usernames, host names, device IDs, serial numbers, tokens, and private terminal output before submitting.

An Inlaid support report is created locally only when requested and is never uploaded by the app. Review it before attaching it. Security vulnerabilities and sensitive proof of concept belong in a [private security advisory](https://github.com/Melty1000/inlaid/security/advisories/new), not a compatibility issue.

For an ordinary compatibility test, use the normal Inlaid application; there is no first-run test, tester mode, or separate executable. Exercise the camera and outputs first, then select **Report** and confirm with **Create Report**. Inlaid writes one bounded JSON file to the active layout's resolved support-report directory; installed, portable, source, and explicit-test modes retain their distinct directory contracts. Select **Open Folder** to open that resolved directory, inspect the JSON as text, and remove anything you do not want public. Only after separate explicit authorization to make that external mutation may a tester open the compatibility form and attach the JSON file. Inlaid does not create a ZIP, upload the report, or create an issue.
