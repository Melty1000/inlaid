# macOS distribution contract

Status: **Accepted by Cody on 2026-08-27**

Applies to: the first native macOS distribution governed by this contract

Decision owner: Cody

This document settles the macOS artifact, installation, command, lifecycle,
data, camera-permission, dependency, signing, notarization, provenance, and
evidence boundaries required before Inlaid can publish a macOS build. Contract
acceptance is policy acceptance only. It does not accept an implementation, an
installer package, a terminal, a camera, a compatibility claim, Apple Developer
enrollment, signing or notarization work, or a publication action.

The accepted [Windows distribution contract](DISTRIBUTION.md) and accepted
[Linux distribution contract](DISTRIBUTION-LINUX.md) remain platform-owned.
Shared release identity, logical payload roles, evidence discipline, and
authorization boundaries are reused where genuinely common. Windows MSI,
user-`PATH`, and known-folder rules and Linux Debian, APT, and XDG rules do not
become macOS rules. Acceptance of this contract replaces only provisional
future-macOS and generic non-Windows placeholders in those documents and their
cross-document consumers; it would not weaken or reinterpret an accepted
Windows or Linux guarantee.

## Current evidence and unsupported baseline

There is no published macOS artifact, package, Homebrew formula, or release
route. The immutable source evidence accepted by the native portability review
is commit `adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3` and native CI run
[`32782751639`](https://github.com/Melty1000/inlaid/actions/runs/32782751639).
That baseline has:

- an experimental cgo bridge to AVFoundation that selects an exact device by
  AVFoundation unique ID, requests camera authorization, configures an exact
  source mode and cadence, and delivers bounded reduced NV12 frames with color
  range and matrix metadata;
- an embedded `__TEXT,__info_plist` whose `CFBundleIdentifier` is
  `com.melty1000.inlaid` and whose camera purpose string is
  `Inlaid uses the camera you choose to render live video in your terminal.`;
- a `macos-15` native CI job that runs on GitHub's Apple-silicon runner, uses
  Go 1.26.7, runs tests, vet, vulnerability scanning, builds `bin/inlaid`, and
  validates the embedded camera declaration;
- the existing source and test layout on non-Windows systems, not an installed
  macOS Library layout;
- an existing folder-opening adapter that invokes PATH-resolved `open` through
  `exec.CommandContext(ctx, "open", path)`, plus optional FFmpeg discovery
  through `INLAID_FFMPEG` or `PATH`; and
- Windows-only packaging and publication. There is no `packaging/macos`
  implementation, macOS package lifecycle test, signed candidate, notarization
  route, or macOS release job.

The complete immutable baseline and its physical limits are recorded in the
[native portability review](reviews/native-portability-local-review.md).
Uncommitted runtime-layout and payload work is provisional, not evidence of a
macOS package. GitHub currently maps `macos-15` to its arm64 macOS 15 runner,
but that label identifies a moving image; candidate evidence must retain the
exact resolved image, hardware architecture, OS build, Xcode, and SDK rather
than treating the label as immutable. See GitHub's current [runner-image
inventory][github-runner-images].

## Distribution decision

The first macOS binary distribution is deliberately narrow:

1. The only initial binary target is **native Apple silicon `arm64` with an
   explicit macOS 15.0 deployment target**. The package's install check rejects
   an Intel Mac and any macOS version below 15.0 before changing the payload.
   Intel `x86_64`, universal binaries, Rosetta-only use, and macOS 14 or earlier
   are unclaimed. A 15.0 deployment target is a build boundary, not proof of
   every macOS 15 point release or a later macOS major version; public support
   wording remains limited to physical evidence recorded in
   [COMPATIBILITY.md](COMPATIBILITY.md).
2. One **Developer ID-signed, notarized, and stapled flat installer package
   (`.pkg`)** is the canonical artifact. It is a normal self-contained Apple
   Installer package: a user may open the exact downloaded package in Finder or
   pass that same file directly to `/usr/sbin/installer`. The package installs
   one stable command under `/usr/local`, so installation and removal request
   administrator authorization. Inlaid itself always runs as the invoking
   unprivileged user. Apple documents opening an installer package in Finder as
   a normal distribution path and uses Installer packages for products that
   place files in specific locations; see [Apple's packaging guidance][apple-packaging].
3. **GitHub Releases is the initial discovery and artifact channel.** The
   version-specific package, checksum manifest, provenance, notarization
   evidence, and release notes belong to one protected-tag release. If platform
   assets share a version and immutable release, all are built from that tagged
   commit and attached to the draft before publication. A platform asset ready
   only after publication uses a later version, tag, and release; it is never
   appended to or substituted into the published immutable release.
4. There is no in-app updater. Users install a newer `.pkg` to update. The
   package and application never poll for, download, or execute an update.
5. There is **no app bundle, DMG, ZIP, tarball, Homebrew formula or cask,
   MacPorts port, Mac App Store product, self-installing shell script, or
   prebuilt portable binary** in the first distribution. A flat package gives
   this terminal command a native receipt-backed system lifecycle and supports
   signing, notarization, stapling, and direct Installer use without another
   container or auxiliary installer component.
6. Homebrew, MacPorts, the Mac App Store, and other third-party channels are
   deferred. Each would add a separate formula, bottle or port, repository,
   review, update, sandbox, path, or publisher lifecycle. No channel may wrap
   or rebuild the canonical package by implication.

## Package contract

### Identity and payload

- The stable package identifier is `com.melty1000.inlaid.pkg`. The signed
  application executable's stable code-signing identifier and embedded bundle
  identifier are both `com.melty1000.inlaid`.
- Release filenames use
  `inlaid-terminal-webcam-<leading-v-tag>-macos15-arm64.pkg`; the executable
  reports that exact leading-`v` tag through `inlaid --version`.
- The package installs the signed `arm64` Mach-O application directly as
  `/usr/local/bin/inlaid`; package-owned documentation and notices under
  `/usr/local/share/doc/inlaid`; an immutable installation manifest at
  `/usr/local/share/inlaid/installation-manifest.json`; an immutable installed
  ledger at `/usr/local/share/inlaid/installed-payload.json`; and a root-owned,
  signed `arm64` Mach-O uninstall helper at
  `/usr/local/libexec/inlaid/uninstall`.
- The uninstall helper has signing identifier
  `com.melty1000.inlaid.uninstall`. Its signed, read-only release section embeds
  the package ID/version, manifest and ledger schemas, the complete bounded
  payload-leaf allowlist, every stable type/owner/mode/digest expectation for
  non-metadata leaves, and the expected application/helper Team IDs, signing
  identifiers, and designated requirements. It deliberately does not embed its
  own final digest or a final manifest/ledger digest. That signed section is the
  local trust root for ordinary offline uninstall; neither the Installer receipt
  nor package ID alone establishes ownership.
- The package contains a signed `arm64` preinstall guard with signing identifier
  `com.melty1000.inlaid.install-guard`. It is package-internal and is not an
  installed lifecycle command. Apple Installer may execute it only from
  Installer's protected extraction of the exact package being installed. No
  package script or helper may download code or evidence, execute code from a
  user-writable path, or require an out-of-band lifecycle component or
  authorization token.
- The package also carries non-installed, non-executable copies of the
  installation manifest, retained trusted-root JSONL, stable verification policy,
  and verifier identity/version record. They exist solely for independent
  release verification. Normal Finder/Installer use, package scripts, runtime,
  repair, update, and uninstall do not read them as external inputs.
- The package installs no wrapper, alias, shell profile, `/etc/paths.d` entry,
  login item, LaunchAgent, LaunchDaemon, background service, app bundle, Start
  menu analogue, terminal launcher, updater, or file-association handler.
- The package and all installed payload leaves are immutable release content.
  No lifecycle operation writes progress, cleanup markers, mutable trust data,
  or a completed-step journal into the manifest, ledger, helper, application,
  or documentation. Recovery derives state from authenticated release material
  and a fresh non-following receipt/filesystem inventory.

The external installation manifest and its byte-identical package-embedded and
installed copies define every payload leaf and every traversed parent. They
record:

- relative payload role and exact absolute destination;
- expected regular-file type, `root:wheel` owner, and mode for every leaf, plus
  SHA-256 for the application, helper, documentation, notices, and other static
  content leaves;
- for each Mach-O, architecture, Team ID, signing identifier, full designated
  requirement, hardened-runtime state, and entitlements; and
- for every traversed parent, exact directory type, owner, mode, non-root-write
  prohibition, and creation eligibility drawn only from the fixed allowlist
  below; and
- the package ID, version, source commit, build identity, and manifest schema.

The helper is signed first. The manifest is then generated with that final helper
digest and must otherwise be field-for-field equal to the stable expectations in
the helper's signed release section. The immutable ledger is generated last and
records the installed package ID/version, source commit, manifest digest, payload
schema, and release filename in a pinned canonical encoding. The signed helper
pins that ledger schema and all non-derived fields, so it derives the only valid
ledger bytes and digest from the authenticated manifest digest. The manifest
does not record the ledger digest, and the helper does not embed either metadata
digest; therefore no digest is self-referential. Apple Installer installs both
metadata files as immutable payload leaves and no script edits them. The receipt,
manifest, ledger, signed helper, and fresh filesystem inventory must agree before
ordinary lifecycle ownership is trusted.

### Installation scope and privilege boundary

- A user may install by opening the original downloaded `.pkg` in Finder and
  following Installer, or from a terminal with:

  ```sh
  /usr/bin/sudo /usr/sbin/installer -pkg "/absolute/path/to/ORIGINAL.pkg" -target /
  ```

  `ORIGINAL.pkg` is the exact downloaded package. No lifecycle route first
  copies, expands, repackages, or substitutes it, and no custom handshake is a
  prerequisite. Finder/Installer owns the normal graphical flow; the absolute
  `/usr/sbin/installer` route owns the terminal flow.
- Apple Installer is the only interface that creates or updates the package
  receipt. Package code never fabricates, directly writes, or edits receipt
  database files. `/usr/sbin/pkgutil` is used only to query a receipt and to
  forget it during uninstall.
- Installer obtains administrator authorization and owns payload/receipt
  mutation. The preinstall guard runs within that transaction only to validate
  the bounded package and installed-state predicates below. It accepts no path,
  manifest, decision, or deletion authority from another process.
- The guard independently verifies its embedded target manifest, its own code
  signature and expected Team ID/identifier, target architecture and macOS
  version, and every traversed parent and payload leaf using non-following
  filesystem operations immediately before allowing Installer to proceed.
  Failure occurs before payload mutation.
- The package never launches Inlaid after installation. At runtime the command
  drops no privilege because it begins as the invoking user. It must not require
  `sudo`, write package locations, run the uninstall helper, or retain an
  Installer authorization right.
- The package targets `/` and uses only its exact `/usr/local` payload. It does
  not install into `/Applications`, `/Library`, `/opt`, another package-manager
  prefix, or a user home.

### Parent and leaf collision policy

The guard and uninstall helper use descriptor-relative, non-following checks;
security decisions never depend on PATH-resolved tools. Every operation checks
all traversed parents and leaves under:

- `/usr/local/bin/inlaid`;
- `/usr/local/share/doc/inlaid`;
- `/usr/local/share/inlaid`; and
- `/usr/local/libexec/inlaid`.

Rules are fail-closed and testable:

- Every traversed parent, including shared `/usr` and `/usr/local`, must be the
  manifest's expected directory type/owner/mode, must not be a symlink, and must
  not be writable by a non-root actor. Any mismatch blocks before mutation.
  Shared parents are safety-checked but never claimed.
- A clean install requires every package payload leaf absent. It may create
  missing parents only from this exact manifest-declared allowlist:

  ```text
  /usr/local
  /usr/local/bin
  /usr/local/share
  /usr/local/share/doc
  /usr/local/share/doc/inlaid
  /usr/local/share/inlaid
  /usr/local/libexec
  /usr/local/libexec/inlaid
  ```

  `/` and `/usr` must already exist and pass the manifest safety predicate; the
  package never creates them. Starting at the nearest existing verified safe
  ancestor, the signed guard running inside Installer creates one missing
  allowlisted child at a time as `root:wheel` mode `0755`, using exclusive,
  descriptor-relative, non-following operations. Before each creation it
  revalidates the parent; immediately afterward it proves the new child is the
  expected directory with exact owner/mode, is not a symlink, and is not writable
  by a non-root actor before descending or allowing payload mutation. A
  pre-existing object at a component just proven missing, concurrent
  substitution, missing component outside the allowlisted chain, or path outside
  the fixed allowlist fails closed. Apple identifies
  `/usr/local` as available for developer content, unlike the protected remainder
  of `/usr`, but does not supply this package-specific hierarchy; see Apple's
  [file-system protection boundary][apple-file-protection].
- A same-version repair or newer-version update authenticates the installed
  signed helper first, then uses the helper's signed release section to
  authenticate the installed manifest and ledger. The receipt, version mapping,
  and fresh inventory must agree. An exact genuine prior package leaf may be
  replaced by Installer; an expected but absent leaf may be restored. A leaf
  whose type, owner, mode, digest, architecture, Team ID, signing identifier, or
  designated requirement differs is modified or ambiguous and blocks the
  operation before payload mutation.
- When the installed helper or immutable metadata is missing or corrupt, the
  exact same-version package may perform bounded damaged-install repair using
  its embedded manifest only if every present package path exactly matches that
  manifest and the receipt, when present, has a compatible package/version
  identity. Any conflict blocks. Apple Installer alone restores payload and
  creates or updates its receipt.
- Neither root package code nor the uninstall helper copies or moves a modified
  leaf into a home directory or user-selected path. Recovery refuses to
  overwrite it and reports the one exact manifest leaf path and observed
  classification. The privileged lifecycle flow then ends. If the leaf is
  readable, the user may independently make any desired preservation **copy** to
  a user-owned destination while unprivileged; that copy is not lifecycle
  evidence or mutation authority. Because unlink and rename require write access
  to the containing directory, an unprivileged user is not expected to remove,
  rename, or replace the protected package path; see Apple's [unlink][apple-unlink]
  and [rename][apple-rename] interfaces.
- After any optional preservation copy, collision resolution is a distinct,
  informed administrator remediation outside the failed lifecycle attempt. It
  may remove or replace only the exact absolute payload leaf reported by that
  attempt, under parents independently reverified as safe. It must name no root,
  parent directory, wildcard, glob, recursive operation, or additional path; it
  must not follow a leaf or parent symlink, adopt unknown bytes, or infer authority
  from the prior report alone. Unsafe-parent and directory-type collisions remain
  expert-admin failures rather than permission for broad cleanup. The later
  install, repair, update, rollback, or recovery invocation starts separately,
  performs a fresh inventory, and proceeds only if the leaf is now absent or
  exactly genuine. No repository remediation command exists today; its future
  implementation and evidence are planned and separately authorized.
- Uninstall removes only leaves that satisfy the complete locally authenticated
  ownership predicate. A modified, wrong-type, symlinked, missing, or ambiguous
  package-named path is retained and reported as foreign; it is never followed.
- Every parent directory, including package-specific parents, is preserved
  unconditionally. Creation is not inferred from a receipt, BOM, emptiness, or
  static manifest, so uninstall never removes `/usr/local`, a traversed parent,
  or an empty package directory.
- User aliases, functions, shell hashes, PATH entries, Homebrew or MacPorts
  commands, and another `inlaid` elsewhere remain user-owned and unchanged.

### Command discovery

The installed command name is exactly `inlaid` at `/usr/local/bin/inlaid`.
The package does not edit `PATH`. On a normal fresh macOS login shell where
`/usr/local/bin` is already present, `command -v inlaid` resolves the installed
leaf. A customized shell that omits `/usr/local/bin` must either invoke the
absolute path or make its own PATH decision; the installer does not alter shell
configuration. Existing shell command hashes may require a fresh shell or the
shell's own cache refresh.

The package preflight treats an occupied `/usr/local/bin/inlaid` as a collision
unless the full signed-helper, manifest, ledger, receipt, and filesystem
predicate proves it is the exact package-owned installation eligible for repair
or update. It never removes or rewrites a competing command merely because
`command -v` finds it first.

### Version identity

- One leading-`v` tag is the human and executable version. Package version uses
  an Apple Installer-compatible monotonic mapping recorded in the repository
  release ledger; the tag, package version, source commit, package filename,
  manifest, ledger, executable output, notarization submission, attestation,
  checksums, and release notes reconcile before publication.
- A same-version exact package is a repair candidate. A greater mapped version
  is an update candidate. A lesser version is a downgrade and fails before
  mutation while the newer receipt/installation remains.
- Deliberate rollback is uninstall of the newer package followed by normal
  installation of an older retained, still-trusted package. User data is not
  rolled back or removed.
- A new package is never substituted into an existing tag/release. If a platform
  artifact misses a shared immutable release, it receives a later version, tag,
  and release.

## Terminal-first lifecycle

### Normal commands

The package is usable without downloading a separate evidence directory. The
normal routes are:

```sh
# Install, same-version repair, or update with the original target package.
/usr/bin/sudo /usr/sbin/installer -pkg "/absolute/path/to/ORIGINAL.pkg" -target /

# Confirm the installed receipt and command.
/usr/sbin/pkgutil --pkg-info com.melty1000.inlaid.pkg
command -v inlaid
inlaid --version

# Remove an intact installation offline.
/usr/bin/sudo /usr/local/libexec/inlaid/uninstall
```

Opening `ORIGINAL.pkg` in Finder and completing the Apple Installer UI is an
equivalent supported install, repair, or update route. There is no package-owned
GUI application after installation; runtime remains terminal-first.

Advanced users and release validation may additionally verify a retained
release-evidence directory before installation:

```sh
cd "/absolute/path/to/release-evidence"
/usr/bin/shasum -a 256 -c SHA256SUMS.txt
/usr/sbin/pkgutil --check-signature "./ORIGINAL.pkg"
/usr/sbin/spctl --assess --type install --verbose=4 "./ORIGINAL.pkg"
```

Those checks are recommended expert verification and mandatory candidate
evidence, but they are not a hidden prerequisite for Finder or direct Installer
use. The package, scripts, application, and uninstall helper perform no network
download. No command above clears quarantine, changes Gatekeeper, changes TCC,
or executes a package component from a user-writable extraction.

### Install, repair, and update

For a clean install, the embedded guard requires the supported architecture/OS,
no Inlaid receipt, and no occupied package leaf or unsafe parent. It may create
only the fixed allowlisted missing parent chain, one verified `root:wheel` mode
`0755` directory at a time beneath the nearest existing safe ancestor, with the
defined non-following pre/post checks. Apple Installer then installs the complete
payload and creates the receipt. It does not launch Inlaid.

For same-version repair, the user supplies that version's exact package through
Finder or `/usr/sbin/installer`. The embedded guard authenticates the intact
local helper, manifest, ledger, receipt, and filesystem. It permits Installer to
restore a missing leaf or replace an exact genuine leaf, but any modified or
ambiguous package-named path blocks. The user may first preserve a readable copy
unprivileged, but only a later distinct exact-path administrator remediation may
change the protected collision before a fresh repair attempt.

For update, the user supplies only the newer target package. The new package's
embedded guard authenticates its own target manifest and the existing local
trust root, then proves the current payload is exact and the target version is
greater. The target package may replace exact old leaves, restore missing target
leaves, and install new absent leaves. In-place updates may not silently drop a
previous payload destination: the initial package's destination-role set stays
stable, and adding a new absent leaf is permitted only when the target manifest
fully describes it. A future release that must retire a payload path requires
offline uninstall followed by clean installation, or a separately accepted
contract revision; an update script never deletes an obsolete path. Update
installs a complete new signed helper, manifest, and ledger whose
non-self-referential ownership chain agrees. Only Apple Installer updates the
receipt.

The old download directory and external release evidence are not runtime state.
Deleting them after a successful installation does not affect `inlaid` or normal
offline uninstall. A later repair or update naturally requires the package that
is to be installed at that time, but never requires all assets from the old
release.

Installer or script failure produces a nonzero result and bounded diagnostic.
No custom journal is authoritative. The next repair or recovery attempt starts
with a fresh non-following receipt/filesystem inventory. A mixed or ambiguous
state cannot authorize replacement: exact locally authenticated old leaves may
be repaired or updated, target-exact leaves may be retained, and every differing
leaf blocks until explicitly resolved. Tests inject failure before and after
each script/Installer boundary and prove retries never broaden the allowlist.

### Downgrade and rollback

A package whose mapped version is lower than the authenticated installed version
is rejected before payload mutation. There is no implicit downgrade flag.
Deliberate rollback is:

1. preserve any wanted user data (uninstall already preserves it by policy);
2. run the installed offline uninstall helper;
3. install the exact older signed/notarized/stapled package through Finder or
   `/usr/sbin/installer`; and
4. confirm receipt, executable version, command discovery, and retained data.

If uninstall retains a modified or ambiguous package-named leaf, the older
package will also fail closed. After any desired unprivileged preservation copy,
the explicitly reported protected leaf requires the distinct narrow
administrator remediation before the older package is retried from a fresh
inventory. Rollback never rewrites settings or media to an older schema by
implication.

### Offline uninstall

An intact installation must uninstall without its download directory, GitHub,
or any external release-evidence files. The only normal command is:

```sh
/usr/bin/sudo /usr/local/libexec/inlaid/uninstall
```

The helper has no network behavior and accepts no arbitrary deletion root,
manifest path, backup destination, or package ID. Before deleting anything it:

1. non-followingly proves it is a root-owned, single-link, mode-`0755` regular
   Mach-O with the expected Team ID, signing identifier, designated requirement,
   architecture, and valid code signature;
2. reads its signed release section; computes its own final digest; requires the
   root-owned mode-`0644` regular installed manifest to match every signed stable
   expectation and to name that self-digest; computes the manifest digest; then
   derives the one canonical ledger and requires exact ledger bytes and digest;
3. queries the exact receipt with `/usr/sbin/pkgutil` and requires its package
   ID/version to match the signed helper, manifest, and ledger; and
4. inventories every manifest parent and leaf non-followingly and classifies it
   as exact genuine, absent, modified, foreign, or unsafe.

Except for the bounded post-receipt cleanup state defined below, the helper stops
before deletion if its local trust root, receipt, manifest, or ledger is missing
or corrupt. That is a damaged installation, not permission to guess from package
ID or receipt alone. For an intact trust root, uninstall:

1. deletes exact genuine application, documentation, and other ordinary payload
   leaves; reports absent leaves; and retains every modified, foreign, unsafe, or
   ambiguous path without following it;
2. refreshes the inventory and proceeds only when no genuine removable leaf
   other than the helper, manifest, and ledger remains;
3. runs `/usr/sbin/pkgutil --forget com.melty1000.inlaid.pkg`; if it fails or the
   receipt remains queryable, it stops with the helper, manifest, and ledger
   intact so the same command is retryable;
4. after receipt absence is proven, removes the exact immutable ledger and
   manifest, then unlinks the exact running helper last; and
5. preserves every parent directory unconditionally.

If final metadata cleanup fails after receipt forgetting, the signed helper
remains the bounded retry entry when its own unlink failed; its signed embedded
allowlist authorizes a post-receipt cleanup retry only when a fresh inventory
proves the receipt absent, every non-final genuine payload leaf absent, the
helper exact, and the remaining metadata forms one valid remainder of the established
cleanup order. If the manifest remains, the helper validates every stable field
against its signed expectations plus its computed self-digest, computes that
authenticated manifest's digest, and only then derives and validates any
remaining canonical ledger. If the manifest is absent, the ledger must also be
absent; a ledger without its manifest fails closed because its expected bytes can
no longer be derived. Modified or wrong-type final leaves are retained and
reported. The only accepted partial states are exact ledger plus exact manifest
plus helper, exact manifest plus helper after ledger removal, or helper alone
after both metadata leaves were removed. From those states the helper removes
only an authenticated remaining ledger, then manifest, then itself. If the helper
was removed but a metadata leaf remains, the install is damaged and the optional
recovery route below diagnoses that exact remnant. Neither case authorizes broad
directory removal. Tests inject receipt-forget and each final-cleanup failure and
prove no recovery state needed for a still-pending step was deleted early.

Uninstall never removes or edits Application Support, Caches, Movies, Pictures,
Documents, recordings, snapshots, filters, settings, recovery state, support
reports, TCC records, terminal settings, shell configuration, PATH, FFmpeg, or
foreign package-manager content. Modified package-named leaves are reported and
retained as foreign; their bytes do not prevent receipt forgetting once every
genuine package payload leaf has been removed or reclassified by the fresh
inventory. Uninstall never invokes administrator collision remediation. If the
user later wants the retained exact path cleared for reinstall, the optional
unprivileged preservation copy and distinct exact-path administrator action
remain separate from uninstall.

### Damaged-install recovery

Recovery is fail-safe and package-driven, not a second installer architecture.
It uses the exact signed/notarized/stapled current or target `.pkg` through
Finder or `/usr/sbin/installer`; it never executes package-extracted code outside
Installer, creates a privileged package copy, publishes an interprocess
authorization token, or writes a receipt directly.

- A missing application, helper, manifest, ledger, or receipt may be restored by
  the exact same-version package when the embedded guard proves every present
  package path equals its target manifest and every conflicting path is absent.
  Apple Installer alone creates or updates the receipt.
- A failed update may be retried with the target package. Its guard evaluates a
  fresh inventory against both the locally authenticated current identity, when
  intact, and its embedded target manifest. Mixed exact-current and exact-target
  leaves are recoverable; any third value or untrusted local metadata blocks.
- External release evidence for the current and/or target release may be used by
  an expert to verify identity and diagnose a damaged state offline. It does not
  grant additional write/delete authority and is not consumed as a hidden
  normal-lifecycle dependency.
- A modified package leaf is never automatically backed up by root. Recovery
  refuses to overwrite it and prints its exact path and classification. The user
  must leave the privileged flow and may copy any readable wanted bytes to a
  user-owned destination while unprivileged. A separate informed administrator
  action may then remove or replace only that exact reported protected leaf under
  freshly verified safe parents. Recovery accepts no arbitrary root, parent,
  wildcard, glob, recursive cleanup, or automatic adoption. The later package
  retry inventories from scratch and requires the leaf absent or exactly genuine.
- If safe reconciliation cannot be proven, recovery stops with all ambiguous
  paths and user data intact. Manual diagnosis may explain remaining paths, but
  no receipt, BOM, checksum beside an untrusted file, or prior diagnostic is
  deletion or adoption authority.

## Quarantine and Gatekeeper

The normal Installer input is the original downloaded package. Neither the
package nor any documented lifecycle command removes, rewrites, copies, or
synthesizes `com.apple.quarantine`. The contract does not claim that raw xattr
bytes copied to another inode recreate Apple provenance or approval semantics.

Gatekeeper checks downloaded installer packages for identified-developer
signing, notarization, and integrity and may request user approval on first open;
see Apple's [Gatekeeper and runtime protection guidance][apple-gatekeeper]. The
supported graphical route observes that real decision by opening the original
browser-downloaded package in Finder. The terminal route passes the same original
file directly to `/usr/sbin/installer`. Neither route disables Gatekeeper or
uses `spctl` to add an exception.

For ordinary candidate tests, record whether quarantine is absent or present on
the original input and record the actual Gatekeeper/Installer result. For the
required browser-download physical route, quarantine must be present on that
original package before Finder opens it; evidence records presence and a
privacy-safe digest/length of the value, the real approval/assessment result,
and that the attribute on the original was not changed by an Inlaid lifecycle
command. Quarantine metadata is evidence, not part of package SHA-256 identity.

## Installed application data

Installed macOS mode resolves every writable location once through Apple's
Foundation directory APIs in the user domain. Literal `~` concatenation, the
working directory, and the executable directory are not path authorities.
Apple defines Application Support as the standard location for support files
and, for an unsandboxed macOS process, locates it under the current user's
`~/Library/Application Support`; it separately exposes cache, Movies, Pictures,
and Documents directories. See Apple's [Application Support directory
reference][apple-application-support].

| Data | Installed macOS location |
|---|---|
| Settings | user Application Support `Inlaid/inlaid-settings.json`, normally `~/Library/Application Support/Inlaid/inlaid-settings.json` |
| Custom `.cube` filters | user Application Support `Inlaid/Filters` |
| Recording recovery and durable application state | user Application Support `Inlaid/Recovery` |
| Regenerable cache, if later needed | user Caches `Inlaid`, normally `~/Library/Caches/Inlaid` |
| Recordings | user Movies `Inlaid`, normally `~/Movies/Inlaid` |
| Snapshots | user Pictures `Inlaid`, normally `~/Pictures/Inlaid` |
| Support reports | user Documents `Inlaid/Support Reports`, normally `~/Documents/Inlaid/Support Reports` |

Additional rules:

- If Foundation cannot resolve a required user-domain location, installed mode
  fails usefully. It never falls back to the working directory, executable
  directory, `/tmp`, `/usr/local`, `/Library`, or another account's home.
- New Inlaid private state directories are created `0700` and private files
  `0600`. Media and report files default to private creation as well. Existing
  permissions are not silently broadened.
- Application Support contains durable settings, filters, and recovery state;
  Caches contains only content that can be regenerated. Uninstall and repair do
  not treat either location as package payload.
- macOS Files and Folders policy may mediate access to user-visible directories.
  Inlaid requests no blanket Full Disk Access, changes no privacy setting, and
  never treats a denied write as permission to redirect the file silently. It
  keeps recoverable source material where applicable and reports the failed
  destination usefully.
- The existing `--settings` option remains a narrow expert/test override for the
  settings file only. It does not select installed, source, or explicit-test
  mode or derive every other directory from the selected settings path.
- Support reports remain opt-in, bounded, local, and never uploaded by Inlaid.
  The UI exposes the resolved report path even if folder opening fails.

## Source, manual-copy, and migration behavior

- Verified Git tags and GitHub automatic source archives remain source
  acquisition routes, not prebuilt macOS artifacts. Source builders use the
  existing Go build and native-test commands in [TESTING.md](TESTING.md) and
  supply Go 1.26 or newer, Apple Clang, and a compatible macOS SDK.
- After installed-mode implementation, source launches use the explicit source
  root. The macOS resolver checks an explicit source or test root before
  installed mode and never guesses source mode from the working directory, a
  writable executable, `.git`, `go.mod`, or a development version.
- There is no macOS portable marker or prebuilt portable layout. Copying
  `/usr/local/bin/inlaid`, a source-built executable, or a package payload file
  elsewhere does not move data beside it, preserve Gatekeeper or TCC behavior,
  create a supported update route, or become a portable distribution.
- Installation never scans a home directory, Downloads, Desktop, source tree,
  mounted volume, Homebrew prefix, MacPorts prefix, or Windows portable folder
  for old Inlaid data.
- Migration from a source root is explicit, non-destructive, and copy-only while
  Inlaid is closed. Settings copy only when the installed destination is absent;
  top-level `.cube` filters copy without overwriting conflicts. Recordings,
  snapshots, reports, and recovery tapes stay in the source root. Live recovery
  is finished or abandoned with the source build first, and every retained path
  is reported.
- The existing portable-import interface may import a user-selected valid
  marked portable root into the installed macOS layout only after that route has
  implementation tests. It remains copy-only, conflict-reporting, and closed
  around live recovery. It never turns an unmarked source tree or arbitrary
  directory into a portable root.

## Camera, permissions, dependencies, and sandbox boundary

### AVFoundation and TCC

- The release executable is cgo-enabled and links the AVFoundation, CoreMedia,
  CoreVideo, and Foundation system frameworks through Apple Clang and the
  selected macOS SDK. A `CGO_ENABLED=0` build fails honestly and is never a
  camera-capable artifact.
- The exact signed executable at `/usr/local/bin/inlaid` is the intended camera
  requester. Its embedded `NSCameraUsageDescription`, stable identifier, Team
  ID, designated requirement, hardened-runtime setting, and camera entitlement
  are verified before packaging and again from the installed payload.
- The hardened executable carries
  `com.apple.security.device.camera=true`. It carries no microphone entitlement
  because Inlaid captures no audio, and no Photos-library entitlement because
  it writes ordinary files rather than adding media to Photos. Apple requires a
  camera purpose string and camera entitlement before a macOS app requests
  capture authorization. See Apple's [media-authorization
  guidance][apple-camera-authorization] and [camera entitlement
  reference][apple-camera-entitlement].
- Inlaid checks AVFoundation authorization before enumeration or capture and
  requests access only when the status is not determined. The package never
  launches the application, pre-grants access, writes TCC, invokes `tccutil`, or
  asks for Full Disk Access. A denied or restricted result is a useful error;
  the user alone changes Camera access in System Settings.
- A command-line process can expose TCC and System Settings behavior that
  differs by signing state, terminal, launch path, update, and OS version. This
  contract does not invent the displayed permission owner. Physical evidence
  records whether macOS attributes the request to Inlaid, the invoking terminal,
  or another identity, then proves first grant, remembered grant, denial,
  recovery through user action, repair, update, uninstall/reinstall, and path
  collision behavior. No support or persistence claim exceeds that evidence.
- The current adapter selects the exact AVFoundation unique ID, requests a
  supported native format and cadence, produces reduced NV12, validates the
  negotiated configuration, bounds retained frames, and drains callbacks on
  close. Built-in, Continuity, virtual, and external cameras are not generalized
  from source or CI; each claim requires physical evidence. Device unique IDs
  are never published.

### Runtime and optional dependencies

- The package contains native `arm64` Mach-O application and helper binaries.
  They dynamically link only the macOS system libraries and frameworks derived
  from the finished binaries; the package bundles no C runtime, Go toolchain,
  Xcode component, SDK, or third-party dynamic library. Candidate evidence
  records complete load-command and architecture closure and rejects an
  unexpected writable or non-system dependency.
- Go, Apple Clang, Xcode or Command Line Tools, the macOS SDK, package tools,
  and signing/notarization tools are build or release dependencies only. End
  users do not install them to run Inlaid.
- FFmpeg is optional, unbundled, and outside live preview and PNG snapshots.
  Installed Inlaid discovers an executable through `INLAID_FFMPEG` or the
  effective `PATH`, probes it before export, and never downloads or installs it.
  Homebrew, MacPorts, and manual FFmpeg copies remain user-owned; the Inlaid
  package declares no package-manager dependency and never removes them.
- A missing or broken FFmpeg disables MP4 and GIF export with a useful error
  while preserving preview, PNG, and recovery data. No FFmpeg provenance is
  implied by the Inlaid package signature.
- The existing Open Folder adapter invokes PATH-resolved `open`; that is the
  live source and native-CI route, not an absolute-path guarantee. The future
  installed implementation intentionally changes the macOS adapter to invoke
  absolute `/usr/bin/open`, avoiding a user-`PATH` shadow for this system helper.
  This change is planned and requires source, installed, and explicit-test
  evidence. In either route, failure preserves the created media or report and
  shows its path rather than treating creation as failed.

### Sandbox boundary

- The first direct Developer ID distribution uses the hardened runtime but is
  **not App Sandbox-enabled**; `com.apple.security.app-sandbox` is absent. The
  package must access the defined user Library and media directories and launch
  an optional user-selected FFmpeg executable. App Sandbox would replace those
  data and process boundaries with a container and entitlements and therefore
  needs a new accepted contract and migration evidence.
- The Mac App Store is deferred because it requires App Sandbox. Acceptance of
  this direct-package contract neither declares sandboxing impossible nor
  authorizes an App Store submission. See Apple's [App Sandbox
  boundary][apple-app-sandbox].
- Hardened-runtime exceptions such as disabled library validation, unsigned
  executable memory, JIT, debugging, or DYLD environment access are absent
  unless a later implementation proves one narrowly necessary and Cody accepts
  the changed security boundary before publication.

## Shared packaging and release seams

### Runtime-layout seam

The shared `Layout` value remains the only input the dashboard uses for writable
paths. macOS adds a platform-owned resolver with these modes:

```text
explicit source root -> source
explicit test root   -> explicit-test
otherwise            -> installed macOS user-domain layout
```

macOS does not select `portable` because this contract has no portable macOS
artifact. Windows retains installed/portable/source/explicit-test behavior;
Linux retains installed-XDG/source/explicit-test behavior. Capture, rendering,
recording, and support-report code consume resolved paths and do not learn
Foundation directory, `.pkg`, Installer, Gatekeeper, TCC, terminal, or shell
policy.

### Release-payload seam

The provisional shared payload manifest is physically Windows-oriented because
its common executable destination is `inlaid.exe`. It is uncommitted and not
part of the immutable portability baseline. Later macOS implementation deepens
it into shared logical inputs plus platform destination profiles while proving
the accepted Windows and Linux results unchanged.

Shared logical roles are application executable, required license/notices,
documentation, release identity, and payload digest inventory. macOS owns its
destination profile, signed uninstall helper, immutable installed manifest and
ledger, package-internal signed preinstall guard, package scripts, receipt, and
Developer ID/Installer metadata. Windows keeps MSI/PATH resources; Linux keeps
Debian metadata. No platform consumes another platform's path or lifecycle
policy by implication.

## Signing, notarization, checksums, and provenance

- An authorized release workflow builds the `arm64` application, uninstall
  helper, and preinstall guard on a pinned native macOS image with the chosen
  deployment target. It records resolved runner image, hardware, OS build,
  Xcode, SDK, Apple Clang, Go, cgo state, source commit, and dependency closure.
- The application, helper, and guard receive hardened-runtime Developer ID
  Application signatures with secure timestamps, their exact stable identifiers,
  expected Team ID and designated requirements. Entitlements are minimal and
  recorded. The package receives a secure-timestamped Developer ID Installer
  signature.
- The exact final signed package is submitted for notarization, its accepted log
  is retained and reviewed, and the accepted ticket is stapled and validated.
  Package signature, staple, Gatekeeper install assessment, and installed
  application/helper signatures are verified from the final candidate.
- The final package's external installation manifest, retained trusted-root
  JSONL, stable verification policy, and verifier identity/version record are
  byte-identical to their non-executable package-embedded copies.
  `SHA256SUMS.txt` records the final stapled package and the other
  release-evidence files. The final package is the sole GitHub
  artifact-attestation subject; the external manifest is authenticated by
  byte-identical embedding in that Apple-authenticated package plus its checksum,
  not by claiming it is a second attestation subject.
- The release-evidence directory contains exactly eight required leaves for
  release review and optional expert verification: the final `.pkg`, external
  installation manifest, `SHA256SUMS.txt`, GitHub attestation bundle, retained
  trusted-root JSONL, `verification-policy.json`, verifier identity/version
  record, and release-evidence notes. `SHA256SUMS.txt` lists the other seven
  leaves and does not list or hash itself. It is transport-integrity evidence,
  not a trust bootstrap.
- The verification policy contains only stable repository, workflow,
  protected-ref, issuer, and predicate constraints; it does not contain the
  final package digest. The verifier computes SHA-256 from the exact final
  package and requires the attestation subject digest to equal it. Offline trust
  bootstraps by first using macOS's independent trust path to validate the
  Developer ID Installer signature and staple on the final package, then
  inspecting without execution and byte-comparing its embedded trusted root,
  policy, and verifier record to the external copies. A checksum published beside
  the trusted root never bootstraps trust in it.
- Online and offline attestation verification record the exact verifier and
  trust-root versions and enforce repository, workflow, ref, issuer, predicate,
  and final-package subject digest. GitHub's [artifact-attestation
  guidance][github-attestations] and [CLI verification reference][github-cli-attestation]
  govern that evidence. These provenance assets gate publication but are not
  required inputs to Finder/Installer or intact offline uninstall.
- Secrets, certificates, keychains, Apple account state, notary credentials,
  GitHub permissions, and signing settings never enter the repository, package,
  logs, release evidence, or support reports. Their creation or change remains
  separately authorized.

## Evidence required before macOS publication

### Existing evidence

The immutable portability baseline proves source compilation/test behavior on
one recorded GitHub arm64 macOS 15 environment and the source-level camera,
purpose-string, and PATH-resolved `open` routes described above. It does not
prove a package, installation, terminal discovery, Foundation layout, physical
camera, TCC subject, Gatekeeper decision, signing, notarization, lifecycle,
uninstall, or public support claim.

No macOS package builder, lifecycle harness, signed helper/guard, release job,
notarization path, or physical macOS result exists in the repository today.
Every item below remains planned and separately authorized.

### Planned implementation and candidate evidence

Before publication, retained native CI and controlled-Mac evidence must prove:

1. the exact `arm64`/macOS 15.0 deployment target, package ID/version mapping,
   filename, receipt, source commit, executable version, and rejection on Intel
   and pre-15 systems before payload mutation;
2. one self-contained flat package that succeeds both by opening the original
   downloaded file in Finder/Installer and by direct absolute
   `/usr/sbin/installer -pkg ORIGINAL.pkg -target /`, with no auxiliary
   lifecycle executable, package copy or expansion, interprocess authorization,
   external evidence input, or network dependency;
3. the signed package-internal guard's identity and protected Installer-only
   execution; independent target-manifest, OS/architecture/version, receipt,
   traversed-parent, and payload-leaf checks; non-following behavior; and
   pre-mutation failure for every foreign, modified, symlinked, wrong-type, or
   unsafe path;
4. exact manifest and ledger bytes, signed-helper release-policy/allowlist,
   helper-self-digest, and canonical metadata chain agreement without a
   self-reference; receipt agreement; and
   type/owner/mode/digest/signature expectations
   for every application, helper, ledger, manifest, documentation, and notice
   leaf; every traversed parent; the exact eight-parent creation allowlist;
   nearest-safe-ancestor creation with exclusive non-following pre/post checks,
   `root:wheel` mode `0755`, collision/race refusal; and unconditional parent
   preservation;
5. clean install, same-version repair, update, injected partial failure and
   retry, blocked downgrade, uninstall-then-older-package rollback, damaged
   metadata/receipt repair through the exact package, and second install;
   modified-leaf refusal; optional unprivileged preservation copy; separate,
   informed, one-reported-leaf administrator remediation; fresh retry inventory;
   and no automatic backup, adoption, arbitrary root, parent, wildcard, glob, or
   recursive cleanup;
6. offline intact-install uninstall after deleting the download/evidence
   directory: signed-helper self-authentication, local manifest/ledger/receipt
   authentication, exact-leaf deletion only, modified/ambiguous preservation,
   receipt forgetting, final metadata/helper cleanup, failure injection at every
   boundary, and bounded retry without broad deletion;
7. normal lifecycle independence from the optional eight-leaf release-evidence
   directory, and optional expert/damaged-install use that adds verification or
   diagnosis but no mutation authority;
8. bare `inlaid` discovery in a fresh native shell, exact `--version`,
   deterministic `--render-preview`, working-directory preservation, no terminal
   handoff, no PATH/profile edits, command-hash refresh, and useful behavior when
   `/usr/local/bin` is absent from a customized PATH;
9. all Foundation-resolved Application Support, Caches, Movies, Pictures, and
   Documents paths; private creation modes; directory-access denial; no writes
   to package roots at runtime; no working-directory fallback; and preservation
   through repair, update, rollback, uninstall, and reinstall;
10. source migration, marked-portable import, settings/filter conflict handling,
    live-recovery refusal, and no automatic scan, move, overwrite, or deletion;
11. cgo-enabled AVFoundation behavior, embedded purpose string, hardened runtime,
    camera entitlement and absence of unnecessary entitlements, system-only
    dynamic-library closure, plus honest cgo-disabled and missing-framework
    failures;
12. FFmpeg present, missing, invalid, PATH-shadowed, and explicitly configured
    behavior; the existing PATH-resolved `open` route recorded honestly; the
    planned absolute `/usr/bin/open` route proving a PATH shadow is not invoked;
    and helper failure without losing media or reports;
13. Developer ID Application and Installer identities, timestamps, full
    designated requirements, package signature, accepted warning-free
    notarization log, stapling/validation, Gatekeeper assessment, exact final
    package hashes, byte-identical embedded/external manifest, and installed
    signature revalidation;
14. the eight release-evidence leaves; checksum file naming only the other seven;
    macOS trust validation of the final package before its non-executable embedded
    trusted-root/policy/verifier copies authenticate byte-identical external
    copies; stable non-self-referential policy; verifier identity/version;
    final-package computed digest equal to the sole attestation subject;
    online/offline repository/workflow/ref/issuer/predicate verification; and
    exact release-asset reconciliation;
15. original-package quarantine absence/presence recording, no Inlaid lifecycle
    command removing or rewriting it, browser-download presence on the exact
    package opened in Finder, and the real Gatekeeper/Installer decision without
    claiming copied-xattr equivalence; and
16. exact runner image, hardware, OS build, Xcode, SDK, deployment target,
    compiler, Go and cgo state recorded rather than inferred from a moving label,
    with accepted Windows and Linux layout/payload evidence unchanged.

Absolute Apple system paths are used for security and lifecycle decisions,
including `/usr/bin/codesign`, `/usr/bin/shasum`, `/usr/sbin/pkgutil`,
`/usr/sbin/spctl`, and `/usr/sbin/installer`. Tests install hostile same-name
PATH shadows and prove none decides signature trust, package assessment, receipt
state, or payload mutation. PATH-resolved `open` remains only the honestly
recorded current source route until the planned installed `/usr/bin/open` change
is implemented and proven.

### Physical community evidence

Before any positive macOS support or **Hardware verified** claim, and before any
public macOS binary:

- at least one separately authorized community validation exercises the exact
  installed candidate on a physical Apple-silicon Mac running an in-scope macOS
  15 point release;
- a browser-downloaded original package has `com.apple.quarantine` present and
  is opened directly in Finder; Gatekeeper remains enabled; no lifecycle route
  removes or rewrites the attribute; and the report records privacy-safe
  presence/length/value-digest evidence plus the actual first-open and Installer
  decisions without publishing raw origin metadata;
- the same original package is also exercised through the direct absolute
  `/usr/sbin/installer` route, with its actual assessment/result recorded;
- the report records exact OS build and architecture, package and executable
  hashes and versions, receipt, terminal, shell, command resolution, TCC subject
  shown by macOS, first grant, remembered grant, denial and user-driven recovery,
  camera and negotiated mode, grid, cadence, retained memory, PNG, MP4 or GIF
  when FFmpeg is present, recovery, clean exit, camera release, and reopen;
- package lifecycle and cryptographic evidence may come from controlled Macs,
  but native CI cannot substitute for physical Gatekeeper, TCC, camera, and
  terminal observation; and
- unavailable Mac hardware remains community validation, not work silently
  assigned to Cody or the Windows maintainer. Its absence blocks a macOS
  publication claim, not unrelated Windows implementation or validation.

This physical gate does not erase bounded **Expected—unverified** or **Known
incompatible** classifications derived from immutable source evidence. Contract
acceptance does not perform or accept physical validation.

### Publication gate

macOS publication is blocked until all of these are true:

1. Cody has explicitly accepted this contract and its status records that fact;
2. macOS implementation and documentation conform to it and pass independent
   review;
3. accepted Windows and Linux package/layout behavior remains reconciled after
   shared-seam changes;
4. native CI, self-contained Finder/direct-Installer package lifecycle, layout,
   collision, dependency, migration, signing, notarization, Gatekeeper,
   provenance, offline uninstall, and candidate checks pass on the exact commit;
5. required physical community evidence is accepted and
   [COMPATIBILITY.md](COMPATIBILITY.md) states no stronger claim;
6. the final stapled `.pkg`, external installation manifest, checksums,
   attestation bundle, retained trusted-root JSONL, exact verification policy,
   verifier identity/version, verification-engine versions, and release notes
   reconcile; `SHA256SUMS.txt` names the other seven leaves and not itself; the
   final package is the sole attestation subject and the manifest authenticates
   through byte-identical embedding plus its checksum; macOS-authenticated
   embedded trusted-root/policy/verifier copies equal their external copies;
   package signature, staple, notarization log, dependency/entitlement inventory,
   receipt, installed manifest/ledger, and signed-helper local trust root
   reconcile exactly;
7. Apple publisher identity, certificate validity, repository permissions, and
   current GitHub immutable-release state have been reviewed without assuming
   an enrollment, credential, or setting change; and
8. Cody separately authorizes the exact GitHub publication after seeing the
   immutable candidate evidence.

Passing source tests, contract acceptance, an implemented package, notarization,
or a physical report alone is insufficient.

## Authorization boundaries

Acceptance of this document authorizes no implementation and no external
mutation. Package implementation, physical validation, Apple Developer Program
enrollment or fees, legal or publisher identity, certificate creation,
certificate or private-key import/export, keychain changes, App Store Connect or
notary credentials, GitHub secrets or settings, signing, notarization, software
installation, quarantine or Gatekeeper changes, TCC changes, uploads, tags,
releases, issues, Homebrew or MacPorts submissions, App Store submissions, and
public compatibility claims each remain behind their exact later authority.

This phase creates no successor task. Physical macOS evidence remains a
separately authorized community-validation stream and does not block unrelated
Windows work.

## Requirement disposition

| Requirement | Contract owner or exclusion |
|---|---|
| Architecture and OS | Native Apple-silicon `arm64`; explicit 15.0 deployment target; Intel, universal, Rosetta-only, pre-15, and untested later-major claims excluded |
| Native artifact | One planned Developer ID-signed, notarized, stapled, self-contained flat system `.pkg`; no app bundle, DMG, ZIP, tarball, auxiliary installer artifact, or portable binary |
| Discovery/channel | Direct immutable GitHub Release; Homebrew, MacPorts, App Store, and other channels deferred |
| Command and location | Direct `/usr/local/bin/inlaid`; fresh native shell; no wrapper, profile, PATH, `/etc/paths.d`, or terminal edit |
| System/user privilege | Administrator-authorized Apple Installer/uninstall lifecycle; unprivileged application runtime; no per-user installer |
| Collisions | Non-following checks for every payload leaf and traversed parent; signed-helper local trust root plus receipt, immutable ledger/manifest, metadata, digest, and Mach-O-signature predicate; fail closed on modified, foreign, ambiguous, symlink, wrong-type, or unsafe-parent paths; optional preservation is an unprivileged copy, while changing a protected collision requires a separate informed administrator action limited to the one reported exact leaf; preserve every parent and every user-owned shadow |
| Parent creation | Manifest fixes the only eight shared/package parent paths Installer may create beneath the nearest existing verified safe ancestor; each is exclusively created and post-verified `root:wheel` mode `0755` without following; existing/unsafe/foreign objects fail closed; every parent survives uninstall |
| Install/repair | Open the original `.pkg` in Finder or pass it directly to absolute `/usr/sbin/installer`; embedded signed guard independently checks target and installed state and creates only the fixed allowlisted missing parent chain; no external authorization/evidence prerequisite; repair restores only absent or exact-owned leaves; Apple Installer alone creates/updates receipts and never launches Inlaid |
| Update/rollback/failure | New target `.pkg` authenticates exact current state and replaces only genuine leaves; downgrade blocked; rollback is offline uninstall then older package; a protected collision requires optional unprivileged preservation then separate exact-leaf administrator remediation before fresh retry; no authoritative journal, automatic privileged backup/adoption, broad cleanup, or direct receipt writes |
| Uninstall ownership | Intact install uninstalls offline with signed root-owned helper plus locally authenticated immutable manifest/ledger and receipt; only exact genuine leaves are deleted; modified/ambiguous leaves and all parents/user data survive; receipt is forgotten before bounded final metadata/helper cleanup, with injected retry evidence |
| Damaged recovery | Exact current/target package through normal Installer plus optional external verification evidence; no package-extracted lifecycle executable, root package copy, interprocess authorization, or automatic root backup/adoption; user may preserve a readable copy unprivileged, then a distinct informed administrator action may change only the reported exact protected leaf before fresh retry; ambiguity fails closed |
| Quarantine/Gatekeeper | Original downloaded package is the Installer input; lifecycle never removes, rewrites, copies, or synthesizes quarantine; browser-download physical evidence records presence and the real Finder/Gatekeeper/Installer decision |
| User data | Foundation user-domain Application Support and Caches plus Movies, Pictures, and Documents destinations; no working-directory fallback |
| Source/manual behavior | Explicit source/test roots; no prebuilt portable mode; copied binaries do not become a supported channel |
| Migration | Explicit copy-only source migration and tested marked-portable import; no scanning, moving, overwriting conflicts, or live-recovery import |
| AVFoundation/TCC | Exact signed requester, embedded purpose string, hardened runtime and camera entitlement; user-controlled permission; physical evidence owns attribution and persistence claims |
| Build/runtime dependencies | cgo, Apple Clang, SDK, and tools are build-only; installed Mach-Os link only system libraries/frameworks |
| FFmpeg/open helper | Optional user-owned FFmpeg via environment or PATH; current PATH-resolved `open` is evidence only, while planned installed `/usr/bin/open` must ignore PATH shadows and degrade without losing output |
| Sandbox | Hardened and unsandboxed direct distribution; App Sandbox and App Store require a later contract and migration |
| Runtime/payload seams | macOS Foundation adapter and destination profile; accepted Windows and Linux results must remain unchanged |
| Version identity | Exact executable tag plus stable package ID and monotonic repository-ledger receipt mapping |
| Signing/notarization | Developer ID Application plus Installer identities, timestamps, hardened runtime, notarized/stapled final package, exact verification evidence |
| Checksums/provenance | Optional-for-users but publication-required eight-leaf evidence set; checksum file hashes only the other seven; final package sole attestation subject; manifest authenticates through byte-identical embedding plus checksum; macOS-authenticated embedded trusted-root/policy/verifier copies bootstrap byte-identical external copies; stable non-self-referential policy and computed package-digest equality |
| Native versus physical evidence | Exact commit/run baseline is source evidence only; physical Gatekeeper, TCC, camera, and terminal evidence gates positive claims and publication |
| Immutable release timing | Every same-version asset is ready before publication; later-ready platform assets use a later tag and release |
| Publication prerequisites | Eight-part gate above; no single evidence class is sufficient |
| External actions | Every implementation, account, fee, certificate, keychain, secret, setting, install, permission, signing, notarization, upload, submission, and public claim is separately gated |

[apple-app-sandbox]: https://developer.apple.com/documentation/security/app-sandbox
[apple-application-support]: https://developer.apple.com/documentation/foundation/url/applicationsupportdirectory
[apple-camera-authorization]: https://developer.apple.com/documentation/avfoundation/requesting-authorization-to-capture-and-save-media
[apple-camera-entitlement]: https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.security.device.camera
[apple-file-protection]: https://developer.apple.com/library/archive/documentation/Security/Conceptual/System_Integrity_Protection_Guide/FileSystemProtections/FileSystemProtections.html
[apple-gatekeeper]: https://support.apple.com/guide/security/gatekeeper-and-runtime-protection-sec5599b66df/web
[apple-packaging]: https://developer.apple.com/documentation/xcode/packaging-mac-software-for-distribution
[apple-rename]: https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/rename.2.html
[apple-unlink]: https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/unlink.2.html
[github-attestations]: https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations
[github-cli-attestation]: https://cli.github.com/manual/gh_attestation_verify
[github-runner-images]: https://github.com/actions/runner-images#available-images
