# Linux distribution contract

Status: **Accepted by Cody on 2026-08-26**

Applies to: the first native Linux distribution governed by this accepted contract

Decision owner: Cody

This document settles the Linux artifact, installation, update, command, data,
dependency, provenance, and evidence boundaries required before Inlaid can
publish a Linux build. Contract acceptance is policy acceptance only. It does
not accept an implementation, a package, a terminal, a camera, a compatibility
claim, or a publication action.

The accepted [Windows distribution contract](DISTRIBUTION.md) remains
Windows-owned. Shared release identity and payload roles are reused where they
are genuinely common, but Windows installer, PATH, signing, lifecycle, and data
rules do not become Linux rules. This accepted document replaces only
provisional future-Linux and generic non-Windows placeholders in the accepted
contract and its cross-document consumers. It does not replace or reinterpret
any accepted Windows-owned guarantee, matrix row, or evidence boundary.
Acceptance performs only that bounded policy replacement. The distinct
[macOS distribution contract](DISTRIBUTION-MACOS.md) was accepted by Cody on
2026-08-27 and changes no Linux guarantee or publication authority.

## Current evidence and unsupported baseline

There is no published Linux artifact or package. The immutable source evidence
accepted by the native portability review is commit
`adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3` and native CI run
[`32782751639`](https://github.com/Melty1000/inlaid/actions/runs/32782751639).
That baseline has:

- an experimental cgo V4L2 capture adapter that opens local `/dev/video*`
  devices, requires streaming capture and native MJPEG, and decodes through
  libturbojpeg;
- an Ubuntu 24.04 native CI job that installed `libturbojpeg0-dev` and
  `pkg-config`, then runs tests, selected race checks, vet, vulnerability
  scanning, and a `linux/amd64` build;
- an existing source and test layout on non-Windows systems, not an installed
  XDG layout;
- an existing `xdg-open` folder-opening adapter and optional FFmpeg discovery
  through `INLAID_FFMPEG` or `PATH`; and
- Windows-oriented package and release routes only. There is no
  `packaging/linux` implementation, Linux package test, or Linux release job.

The complete baseline and limits are recorded in
[the native portability review](reviews/native-portability-local-review.md).
Uncommitted implementation and distribution seams are provisional and are not
accepted evidence. Those facts establish useful seams, not release support. The
kernel documents
V4L2 capture devices and streaming/MMAP capability separately; Inlaid's current
adapter needs both the capture and streaming capabilities it probes. See the
[V4L2 capture interface][v4l2-capture] and [MMAP streaming
interface][v4l2-mmap].

## Distribution decision

The first Linux binary distribution is deliberately narrow:

1. A **native Ubuntu 24.04 LTS `amd64` `.deb`** is the only initial Linux
   binary artifact. Debian package metadata uses architecture `amd64`; public
   compatibility wording may say Linux x86_64 when it also names Ubuntu 24.04
   LTS. Ubuntu 24.04 is the boundary because it is the recorded native Linux
   baseline and the OS remains in standard maintenance through May 2029. That
   OS window is not a promise that every dependency receives the same free
   security coverage: `libturbojpeg`, `ffmpeg`, and their source packages are in
   Universe, whose security coverage differs from Main and may depend on
   community maintenance or Ubuntu Pro. Candidate evidence records the archive
   component and then-current security status of every runtime dependency. See
   Ubuntu's [release cycle][ubuntu-release-cycle] and [security-coverage
   guidance][ubuntu-security-coverage].
2. **GitHub Releases is the initial discovery and artifact channel.** The
   version-specific `.deb`, checksum manifest, and verifiable build provenance
   belong to one protected-tag release. If platform assets share a version and
   immutable release, every one is built from that tagged commit and attached to
   the draft before the release is published. An asset that becomes ready after
   publication must use a later version, tag, and release; it is never appended
   to or substituted into the published immutable release. There is no Inlaid
   APT repository and therefore no repository-driven Linux update feed in this
   contract.
3. Users install a downloaded local `.deb` with the Ubuntu package-management
   front end after verifying it. The package manager resolves declared Ubuntu
   dependencies and owns the system transaction. Downloading a newer immutable
   `.deb` is the initial update path; Inlaid has no in-app updater.
4. There is **no generic prebuilt Linux tarball, AppImage, Flatpak, Snap, or
   self-installing shell script** in the first distribution. A dynamically
   linked cgo binary is not called portable without tested ABI and dependency
   boundaries. Flatpak and Snap can expose camera devices under some policies,
   but both remain deferred because their confinement, permissions, runtime and
   data layouts, capture-path behavior, and channel ownership are unsettled and
   unverified for Inlaid. This contract makes no blanket claim that they cannot
   support direct V4L2.
5. Debian, Ubuntu derivatives, Ubuntu releases other than 24.04 LTS, musl-based
   distributions, containers, WSL, ChromeOS, SteamOS, and every architecture
   other than `amd64` are unclaimed. Source builds may be attempted there, but
   they are not covered by this binary contract or by an Ubuntu compatibility
   result.
6. An APT repository, Launchpad PPA, Debian or Ubuntu archive submission,
   Flathub, Snap Store, Homebrew-on-Linux, and third-party repositories are
   deferred. Each adds publisher identity, repository signing, metadata,
   review, and update ownership that direct release assets do not settle.

GitHub describes releases as tag-based software distributions with attached
binary assets and also supplies automatic source archives. Those automatic
source archives are source conveniences, not the native `.deb` and not Linux
package evidence. See [About GitHub Releases][github-releases].

## Package contract

### Identity and payload ownership

- The binary package control field is `Package: inlaid-terminal-webcam`,
  architecture is `amd64`,
  package filenames are
  `inlaid-terminal-webcam_<debian-version>_amd64.deb`, and the direct executable
  is `/usr/bin/inlaid`. The longer package identity is collision-resistant while
  preserving the terminal-first command. No wrapper, alias, profile fragment,
  `update-alternatives` entry, desktop file, graphical launcher, background
  service, autostart entry, file association, protocol handler, udev rule, or
  device node is installed.
- Package-owned documentation and notices live below
  `/usr/share/doc/inlaid-terminal-webcam`. The package supplies
  `/usr/share/doc/inlaid-terminal-webcam/copyright` with the required licenses,
  copyright notices, and third-party notices; the Debian packaging changelog as
  `/usr/share/doc/inlaid-terminal-webcam/changelog.Debian.gz`; and the terminal
  command manual as `/usr/share/man/man1/inlaid.1.gz`. Any upstream release notes
  or user documentation included in the payload also live below the package's
  documentation directory and are reconciled with the payload manifest. Any
  application-owned read-only data added later
  lives below `/usr/share/inlaid`. The package never writes `/usr/local`, which
  is reserved for the local administrator under Debian policy. See Debian's
  [operating-system and filesystem rules][debian-os], [copyright
  policy][debian-copyright], and [documentation policy][debian-documentation].
- The `.deb` owns only files listed in its package database and the package
  registration itself. It owns no file under a user's home directory and no
  camera permission, group membership, shell profile, environment variable,
  terminal setting, or FFmpeg installation.
- Package files are root-owned and non-writable by ordinary users. The package
  does not contain settings, recovery tapes, recordings, snapshots, custom
  filters, support reports, caches, locally downloaded tools, or build-tree
  residue.
- The package uses the shared logical payload roles already accepted on
  Windows: one versioned executable, license, required third-party notices, and
  user documentation. Linux supplies a platform destination map rather than
  reusing Windows filename `inlaid.exe` or Windows profile metadata.

Debian policy requires packaged programs on the system PATH to work without
custom environment setup and forbids conflicting programs with the same PATH
name. It also places packaged files under `/usr`, not `/usr/local`. See the
[Debian files rules][debian-files] and [operating-system
rules][debian-os].

### Installation scope and privilege boundary

- The planned Inlaid `.deb` is a **system installation**. Package installation,
  reinstall, upgrade, downgrade, removal, and purge run through the system
  package manager and therefore require the authority that package manager
  requests. There is no Inlaid per-user installer.
- The package manager never launches Inlaid. Normal application use is as the
  invoking unprivileged user; user documentation never tells users to run the
  dashboard with `sudo` merely to obtain camera access.
- The package does not add the user to a device-access group, change `/dev`
  ownership or modes, install a udev rule, load a kernel module, or enable a
  portal. Camera permission remains host policy. A permission failure produces
  a bounded useful error and no claim that the package repaired the host.
- The only permitted maintainer script is a narrowly scoped `/bin/sh` `preinst`
  collision guard. It uses shell built-ins plus `dpkg-query` and `dpkg-divert`
  from the Essential `dpkg` suite; it cannot rely on its not-yet-unpacked
  payload, package dependencies, networking, or a controlling terminal. For the
  documented `install` and `upgrade` forms—including reinstall and downgrade—it
  first runs `dpkg-divert --listpackage /usr/bin/inlaid` and rejects any nonempty
  result, including `LOCAL`, then treats the path as
  present when either `test -e` or `test -L` succeeds so a dangling symlink is
  not missed. An absent, undiverted path is allowed. A present path is allowed
  only when `dpkg-query --search /usr/bin/inlaid` reports the exact package
  identity `inlaid-terminal-webcam` as its sole owner; an unowned path or any
  other/additional owner fails before
  unpack. The old-script `abort-upgrade` form exits without mutation, and an
  unknown form fails safely. The guard never removes, follows, adopts, or
  diverts a path. There is no `postinst`, `prerm`, or `postrm`. The guard is
  noninteractive, idempotent, network-free, and never traverses a home
  directory. Debian policy limits what `preinst` may assume and requires
  maintainer scripts to be idempotent because operations can be retried after
  interruption. See [maintainer-script policy][debian-maintainer-scripts],
  [`dpkg-query` ownership lookup][dpkg-query], and [`dpkg-divert`
  inspection][dpkg-divert].

### Command discovery and collisions

- Installed use is terminal-first: after installation, open a fresh native
  Linux terminal and shell and run `inlaid`. `/usr/bin` supplies discovery;
  neither the package nor the application edits `PATH` or a shell startup file.
- Inlaid keeps the caller's terminal and working directory. It does not select,
  install, or launch a terminal or shell. The terminal must provide the
  interactive and truecolor behavior described in
  [COMPATIBILITY.md](COMPATIBILITY.md), and an exact host becomes supported only
  from recorded physical evidence.
- A different package already owning `/usr/bin/inlaid` is a package-file
  collision. The
  project package initially declares no `Provides`, `Replaces`, `Conflicts`,
  or alternatives relationship with another package and must not force the
  overwrite. A distro package named `inlaid` is therefore a distinct dpkg
  identity and cannot co-install while it owns the same command.
- dpkg has no publisher identity inside a package name. If a distro or third
  party reuses `inlaid-terminal-webcam`, dpkg treats it as the same package and
  may consider its ordered version an upgrade or downgrade. This contract never
  promises to distinguish those publishers. The direct-download instructions
  require artifact provenance before installation; a future project repository
  must retain this package identity, define APT source/pinning and signing
  policy, and prove migration. It cannot rely on dpkg to distinguish a
  project-built package from a same-name third-party build.
- An unowned pre-existing `/usr/bin/inlaid` is also a collision. Package
  validation must prove the chosen packaging route fails closed before
  overwriting it, or the package cannot be published.
- A user-owned executable, alias, function, or shell hash may resolve before
  `/usr/bin/inlaid`. Installation does not delete, rename, reorder, or adopt it.
  Acceptance evidence records `command -v inlaid`, the shell's all-resolution
  view, and `/usr/bin/inlaid --version` without publishing private paths. A
  collision is resolved by the user; it is not a successful discovery result.

### Version identity

- The executable reports the exact Git tag, including its leading `v`, through
  the existing `--version` interface. The `.deb` maps that identity to Debian's
  ordered version syntax: remove the leading `v`, replace the separator before
  a SemVer prerelease with `~`, and append packaging revision `-1`. For example,
  `v0.3.0-beta.1` maps to `0.3.0~beta.1-1`, while `v0.3.0` maps to
  `0.3.0-1`.
- The mapping is deterministic, recorded with the release, and checked both
  ways. A packaging-only rebuild for the same source increments the Debian
  revision and never reuses an already published package version for different
  bytes.
- Debian's comparison rules make `~` sort before even the end of a version
  component, which keeps prereleases below the corresponding stable release.
  See Debian's [Version field rules][debian-version].

## Lifecycle contract

### Install and repair

- The planned normal local routes are Ubuntu package-manager commands, not
  repository scripts. Replace `VERSION` with the verified Debian version in the
  downloaded filename and run them from its directory:

  ```text
  sudo apt install ./inlaid-terminal-webcam_VERSION_amd64.deb
  sudo apt install --reinstall ./inlaid-terminal-webcam_VERSION_amd64.deb
  sudo apt install --allow-downgrades ./inlaid-terminal-webcam_VERSION_amd64.deb
  sudo apt remove inlaid-terminal-webcam
  sudo apt purge inlaid-terminal-webcam
  ```

  The first form covers clean install and installation of a newer local
  version; `--reinstall` is same-version repair; `--allow-downgrades` is reserved
  for a deliberate verified downgrade. Users inspect APT's proposed transaction
  before authorizing it.
- A clean attempt validates the artifact before invocation, then lets APT and
  dpkg resolve declared dependencies and unpack/configure the allowlisted
  payload. It is not atomic: files may already have been unpacked, and failure
  can leave `Unpacked`, `Half-Configured`, or `Half-Installed` state. Evidence
  records the actual dpkg state and filesystem after each successful and
  injected-failure path rather than inferring success or rollback from the
  front-end exit alone. No successful route leaves an application process
  running.
- Reinstalling the same verified `.deb` is the repair route. It restores
  package-owned files and metadata but does not inspect, rewrite, or delete user
  data. Inlaid has no separate repair command.
- A package failure is not described as an automatic rollback. Debian documents
  that some maintainer-script failures can leave a package half-configured.
  Therefore the first package avoids nonessential maintainer scripts, records
  the actual package state after injected failures, and documents recovery by
  completing or reinstalling a verified package. It never claims MSI-style
  transactional rollback. See [maintainer-script policy][debian-maintainer-scripts].

### Upgrade, downgrade, and channel coexistence

- Installing a newer verified `.deb` is an in-place package upgrade. It replaces
  package-owned files, preserves all user-owned XDG and media paths, keeps the
  command name, and verifies the executable and package versions after the
  transaction.
- There is no background check or automatic download. GitHub discovery is
  manual until a separately accepted repository channel exists.
- Downgrade requires a deliberate package-manager request using a retained,
  verified older artifact. Release notes must identify settings incompatibility
  before such a downgrade. Absent that warning, one previous Inlaid release
  must read current settings safely.
- The local commands above remain the normal direct-channel routes. If an APT
  repository is accepted later, it uses the same `inlaid-terminal-webcam`
  identity and must specify repository signing, source priority/pinning,
  transition from local files, and same-name third-party consequences before it
  becomes an update route.
- An installed project-channel `.deb`, a source build, and user-managed copies may
  coexist only as distinct filesystem roots. Shell resolution determines which
  executable runs. No channel rewrites another channel's files or data, and
  evidence never treats a shadowed executable as the package candidate.

### Remove and purge

- Remove and purge delete only package-database-owned system files and package
  metadata. Neither action traverses a home directory or removes settings,
  filters, recovery state, media, reports, caches, or a user-managed executable.
- The package owns no conffile under `/etc`; purge therefore has no broader
  Inlaid data authority than remove. Ubuntu's APT documentation explicitly says
  purge does not affect data or configuration stored in a home directory. See
  the Ubuntu 24.04 [APT manual][ubuntu-apt].
- Dependency cleanup remains the package manager's decision. Documentation
  may mention `sudo apt autoremove` only as an optional, separate, user-reviewed
  operation after inspecting the proposed list; it is not part of remove or
  purge, and Inlaid never invokes it. Remove and purge never traverse or delete
  any home or XDG path.

## Installed application data

Linux installed mode resolves every writable location once through the shared
runtime-layout seam. XDG environment values are accepted only when absolute;
relative values are ignored and the documented default is used. The XDG Base
Directory specification defines separate config, data, state, and cache homes,
requires absolute overrides, and gives the defaults below. See the [XDG Base
Directory specification][xdg-base].

| Data | Installed Linux location |
|---|---|
| Settings | `$XDG_CONFIG_HOME/inlaid/inlaid-settings.json`; default `$HOME/.config/inlaid/inlaid-settings.json` |
| Custom `.cube` filters | `$XDG_DATA_HOME/inlaid/filters`; default `$HOME/.local/share/inlaid/filters` |
| Recording recovery | `$XDG_STATE_HOME/inlaid/recovery`; default `$HOME/.local/state/inlaid/recovery` |
| Regenerable cache, if later needed | `$XDG_CACHE_HOME/inlaid`; default `$HOME/.cache/inlaid` |
| Recordings | configured `XDG_VIDEOS_DIR/Inlaid`; otherwise `$XDG_DATA_HOME/inlaid/recordings` |
| Snapshots | configured `XDG_PICTURES_DIR/Inlaid`; otherwise `$XDG_DATA_HOME/inlaid/snapshots` |
| Support reports | configured `XDG_DOCUMENTS_DIR/Inlaid/Support Reports`; otherwise `$XDG_STATE_HOME/inlaid/support-reports` |

The user-dir names come from `$XDG_CONFIG_HOME/user-dirs.dirs`. Inlaid parses
only recognized double-quoted assignments. It accepts either a supported
absolute path or the supported `$HOME` form followed by an empty or slash-led
suffix, substitutes the already-resolved absolute home directory, and then
requires an absolute result. It does not source the file, expand another
variable, interpret command substitution, accept `~`, or execute shell code. A
missing, disabled, relative,
malformed, or home-equal media user directory uses the listed XDG fallback
instead of placing files directly in `$HOME`. The xdg-user-dirs project defines
the config location and says pointing a directory to the home directory disables
it. See [xdg-user-dirs][xdg-user-dirs].

Additional rules:

- If neither an absolute XDG override nor an absolute home directory can
  establish a required base, installed mode fails usefully. It never falls back
  to the working directory, executable directory, `/tmp`, or a root-owned path.
- New Inlaid config, data, state, cache, media, and support-report directories
  are created private (`0700`); new settings, filters, recovery tapes, media,
  and reports are created private (`0600`). Existing directory permissions are
  not silently broadened. A user may deliberately change permissions later;
  Inlaid does not broaden them on upgrade.
- The application does not create empty cache or data directories until it has
  content for them. Cache is always disposable; settings, filters, recovery,
  media, and reports are not cache.
- `XDG_RUNTIME_DIR` is not used for recovery or other persistent content. The
  XDG specification binds it to login lifetime and permits cleanup, so it is
  unsuitable for recoverable recordings.
- The existing `--settings` option remains a narrow expert/test override for the
  settings file only. It does not change the distribution mode or derive every
  other location from the chosen settings directory.
- Support reports remain opt-in, bounded, local, and never uploaded by Inlaid.
  The UI exposes the resolved report path even if `xdg-open` is unavailable.

## Source and migration behavior

- Verified Git tags and GitHub's automatic source archives remain source
  acquisition routes, not prebuilt Linux artifacts. Source builders use the
  existing Go build and native-test commands in [TESTING.md](TESTING.md).
- Linux source builds require Go 1.26 or newer, a C compiler, `pkg-config`, and
  libturbojpeg development files. Those are build dependencies and are not
  installed by the runtime `.deb`.
- After installed-mode implementation, source launches use the existing explicit
  source-root interface. The Linux resolver checks an explicit source or test
  root before installed mode and never guesses source mode from the working
  directory, a writable executable, `.git`, `go.mod`, or a development version.
- There is no Linux portable marker or prebuilt portable layout in this
  contract. Merely copying `/usr/bin/inlaid` elsewhere does not move data beside
  the executable or change ownership.
- Installation never scans `$HOME`, the current directory, Downloads, Desktop,
  source checkouts, mounted drives, or Windows portable folders for old data.
- Migration from a source root is explicit, non-destructive, and copy-only:
  while Inlaid is closed, settings may be copied only when the XDG destination
  is absent, and top-level `.cube` filters are copied without overwriting
  conflicts. Recordings, snapshots, reports, and recovery tapes stay in the
  source root. Live recovery is finished or abandoned with the source build
  first. Documentation reports every retained location; no automatic delete or
  move is permitted.
- The existing portable-import interface may import a user-selected, valid
  marked portable root into the installed XDG layout only after that Linux route
  has implementation tests. It remains conflict-reporting and refuses live
  recovery. It does not turn an unmarked source tree into a portable root.

## Runtime dependencies and permissions

### V4L2 and libturbojpeg

- The release binary is cgo-enabled and dynamically linked. The package build
  derives its exact shared-library `Depends` from the finished ELF binary rather
  than hard-coding a development-package name. Debian policy requires compiled
  binaries to calculate dependencies from the libraries actually used, normally
  through `dpkg-shlibdeps`. See [Debian shared-library
  policy][debian-shared-libraries].
- The expected native dependency is Ubuntu's TurboJPEG runtime library; the
  compiler, `pkg-config`, headers, and `libturbojpeg0-dev` remain build-only.
  Ubuntu 24.04 publishes the runtime as package `libturbojpeg`. See the Ubuntu
  [runtime package record][ubuntu-turbojpeg].
- `libturbojpeg` and `ffmpeg` are published from Ubuntu's Universe component.
  Ubuntu 24.04's OS maintenance date therefore does not by itself promise five
  years of free Canonical security maintenance for them. Candidate evidence
  records their component and then-current Ubuntu security/Pro coverage and
  does not convert optional Ubuntu Pro into an Inlaid installation requirement.
- No `v4l-utils` runtime dependency is claimed by the current direct ioctl
  implementation. The host kernel and device must expose V4L2 capture and MMAP
  streaming, a stable device identity, and a native MJPEG mode within the bounds
  in [COMPATIBILITY.md](COMPATIBILITY.md).
- Permission to open the selected `/dev/video*` node is a runtime prerequisite,
  not package ownership. PipeWire camera portals, libcamera-only devices,
  sandbox-only devices, and raw-only cameras remain incompatible with the
  current adapter.

### FFmpeg and desktop integration

- FFmpeg is optional, unbundled, and outside live preview and PNG snapshots.
  The package declares Ubuntu package `ffmpeg` only as `Suggests`, never
  `Depends`; installed Inlaid discovers a working executable through
  `INLAID_FFMPEG` or `PATH` and never downloads one. Ubuntu 24.04 provides an
  `ffmpeg` package, but its presence does not become part of camera support. See
  the Ubuntu [FFmpeg package record][ubuntu-ffmpeg].
- A missing or broken FFmpeg disables MP4 and GIF export with a useful error
  while leaving preview and PNG available. Recovery tapes that require FFmpeg
  remain intact until conversion can succeed.
- The existing Open Folder feature invokes `xdg-open`. Package `xdg-utils` is a
  `Recommends`, not a camera or recording dependency. If it is absent or fails,
  Inlaid preserves the report and shows its path rather than treating the report
  creation as failed. Ubuntu 24.04 provides `xdg-utils`; see its [package
  record][ubuntu-xdg-utils].

## Shared packaging and release seams

### Runtime-layout seam

The shared `Layout` value remains the only input the dashboard uses for writable
paths. Linux adds a platform-owned resolver with these modes:

```text
explicit source root -> source
explicit test root   -> explicit-test
otherwise            -> installed XDG layout
```

Linux does not select `portable` because this contract has no Linux portable
artifact. Windows retains its accepted installed/portable/source/explicit-test
rules. Capture, rendering, recording, and support-report code consume resolved
paths and do not learn XDG, `.deb`, APT, terminal, or shell policy.

### Release-payload seam

The repository's provisional shared payload manifest is logically reusable but
physically Windows-oriented: its common executable destination is
`inlaid.exe`. This uncommitted seam is not part of the immutable portability
baseline. Linux implementation must deepen it into shared logical
inputs plus platform destination profiles while proving the Windows-resolved
payload is unchanged. Linux-specific packaging lives under the planned
`packaging/linux` directory; it does not add Debian metadata to the Windows
profile or teach application code about packaging.

The Linux profile contains the one ELF executable and required documentation
only. It excludes FFmpeg, libturbojpeg, settings, filters, recovery, media,
reports, caches, `.tools`, source launchers, source-control metadata, and local
build residue. Runtime libraries are system package dependencies, not copied
payload.

## Build identity, checksums, signing, and provenance

- A protected tag workflow builds the cgo-enabled ELF once on an Ubuntu 24.04
  `amd64` environment, verifies its exact `Inlaid <tag>` identity, then
  consumes that same ELF in the `.deb`. No local artifact or channel-specific
  application binary is uploaded.
- GitHub's `ubuntu-24.04` hosted label is moving, not a pinned image. Until a
  genuinely pinned build environment exists, candidate evidence records the
  exact resolved runner image version from the job's `Set up job` metadata.
  Go, actions, native build packages, package tooling, and provenance actions are
  pinned or otherwise recorded at exact resolved versions. The build records
  the commit, dirty state (which must be false), tag, Go version, runner image,
  architecture, cgo state, compiler, linked-library closure, package metadata,
  resolved payload, and artifact hashes.
- `SHA256SUMS.txt` covers the `.deb` and any separately published Linux
  verification material. The release workflow verifies the checksum against the
  final uploaded bytes before publication.
- Linux signing is a **cryptographically verifiable GitHub Actions artifact
  attestation bound to the repository and protected workflow identity**, not an
  invented embedded ELF signature and not APT repository signing. GitHub states
  that artifact attestations establish where and how a binary was built and can
  be verified with GitHub CLI. See [GitHub artifact
  attestations][github-attestations].
- Online attestation verification must constrain all of: repository
  `Melty1000/inlaid`, signer workflow
  `Melty1000/inlaid/.github/workflows/release.yml`, signer-workflow digest,
  source ref `refs/tags/<leading-v-tag>`, source commit digest, SLSA provenance
  predicate, and artifact SHA-256. Candidate evidence retains the exact
  successful `gh attestation verify` invocation and JSON result using
  `--repo`, `--signer-workflow`, `--signer-digest`, `--source-ref`, and
  `--source-digest`; repository-only verification is insufficient.
- Offline evidence retains both the downloaded attestation bundle and the
  trusted-root JSONL obtained while online. Offline verification supplies them
  with `--bundle` and `--custom-trusted-root` and enforces the same repository,
  signer-workflow, signer digest, source ref, source digest, predicate, and
  artifact constraints. A bundle without its trusted root or without the same
  identity constraints is insufficient. See the GitHub CLI
  [verification policy][github-cli-attestation] and [trusted-root
  command][github-cli-trusted-root].
- Repository immutable-release capability is checked before publication. If it
  is available but not enabled, enabling it is a separately authorized external
  setting change; this contract does not perform it. Regardless of that setting,
  publication is one-way: every same-version platform asset is attached to the
  draft before publication. An existing tag, release, asset name, or package
  version is never replaced with different bytes. GitHub documents that its
  immutable-release feature locks the associated tag and release assets after
  publication. See [GitHub immutable releases][github-immutable-releases].
- “Reproducible from a verified commit” means inputs, recipe, identity, payload,
  and dependency closure are inspectable and a second unsigned build can be
  compared. Runner metadata and signed attestations may prevent every envelope
  from being byte-identical; the project makes no stronger reproducibility
  claim than its evidence.

An APT repository would require signed repository metadata and new key,
rotation, expiry, compromise, mirror, and update rules. None exists here, so a
detached GPG signature or repository key is not implied by accepting this
direct-download contract.

## Evidence required before Linux publication

### Existing evidence

- The source gate at commit
  `adb0942ac57e93f5d79c3b71e52ffa4c58dd21a3` and Ubuntu 24.04 native CI run
  [`32782751639`](https://github.com/Melty1000/inlaid/actions/runs/32782751639)
  are the immutable existing baseline recorded in the
  [native portability review](reviews/native-portability-local-review.md).
- The opt-in V4L2 physical-test command in [TESTING.md](TESTING.md) exists now.
  The dashboard command is not a ready Linux route: its live recording test
  forces the source tree's `.tools/ffmpeg/bin/ffmpeg`, and the repository has no
  Linux provisioner for that prerequisite. Supplying distro FFmpeg on `PATH` or
  through `INLAID_FFMPEG` does not satisfy it as written.
- Those routes do not build, install, upgrade, sign, attest, or publish a
  `.deb`, do not exercise an installed XDG layout, and do not prove a physical
  camera or terminal on Linux.

### Planned implementation evidence

Before a Linux package can become a release candidate, repository-owned routes
must exist and prove all of the following in a clean Ubuntu 24.04 LTS `amd64`
environment:

1. exact package name, architecture, Debian-version mapping, metadata,
   dynamically derived dependencies, file ownership, modes, and allowlisted
   payload;
2. absence of wrappers, launchers, profiles, services, udev rules, bundled
   libraries, bundled FFmpeg, user data, and undeclared files;
3. the exact documented APT forms for clean install, noninteractive install,
   same-version reinstall, upgrade, explicit downgrade, injected
   failure/recovery, remove, purge, and a second install after removal;
4. fail-closed system-path collisions, including foreign owners, unowned files,
   dangling symlinks, and diversions; allowed same-package repair/upgrade;
   dpkg-state/filesystem reconciliation after failure; and non-destructive
   user-PATH collisions;
5. bare `inlaid` discovery in a fresh shell, exact `--version`, deterministic
   `--render-preview`, working-directory preservation, and no terminal handoff;
6. installed XDG defaults and absolute overrides; rejection of relative and
   malformed overrides; disabled/malformed user dirs; missing home/base
   failures; directory and file permissions; and no writes under `/usr` while
   the app runs;
7. settings/filter migration, portable import, preservation of recovery and
   media, and complete preservation across reinstall, failed upgrade, remove,
   and purge;
8. camera behavior with required libraries present; useful permission, missing
   device, unsupported format, and already-owned-device failures; missing cgo
   or runtime-library failure before a false camera claim;
9. FFmpeg present, missing, invalid, and explicitly configured behavior, plus
   missing/failing `xdg-open` behavior without losing a support report;
10. versioned ELF identity, package-to-ELF identity, dependency closure and
    Ubuntu component/security-coverage record, checksum, provenance fields,
    online repository/workflow/ref/digest-constrained attestation verification,
    offline bundle plus retained trusted-root verification under the same
    constraints, resolved runner-image version, and exact release-asset
    reconciliation; and
11. source mode and explicit-test mode remaining independent from the installed
    XDG layout, with the accepted Windows layout and payload results unchanged.

The APT commands above are existing operating-system interfaces. No Linux
package builder, maintainer-script implementation, lifecycle harness, or release
route exists in this repository; each remains planned until later implementation
adds and validates it. Naming an OS command is not evidence that a repository
route exists or passed.

### Physical community evidence

Before any positive Linux support or **Hardware verified** claim, and before any
public Linux binary:

- at least one separately authorized community validation must exercise the
  exact installed candidate on a physical Ubuntu 24.04 LTS `amd64` machine;
- the report must record the exact point release, kernel, package and ELF hashes,
  package version, camera and negotiated native MJPEG mode, `/dev` permission
  state, terminal, shell, command resolution, grid, cadence, retained memory,
  PNG, MP4 or GIF when FFmpeg is present, recovery, clean exit, camera release,
  and reopen behavior;
- package lifecycle and XDG evidence may come from controlled machines, but
  native CI cannot substitute for the physical camera/terminal observation; and
- unavailable hardware remains community validation, not work silently assigned
  by this contract. A missing report blocks a Linux publication claim, not
  unrelated local implementation or accepted Windows work.

This physical gate does not prevent bounded **Expected—unverified** or **Known
incompatible** classifications derived from the pinned source evidence. It
prevents upgrading those classifications to positive support/hardware claims and
prevents public binary publication without the required observation.

Contract acceptance does not perform or accept this physical validation.

### Publication gate

Linux publication is blocked until all of these are true:

1. Cody has accepted this contract in the document;
2. Linux implementation and documentation conform to it and pass independent
   review;
3. existing Windows package/layout behavior remains reconciled after the shared
   payload change;
4. native CI, package lifecycle, XDG, dependency, collision, migration, and
   candidate checks pass on the exact commit;
5. required physical community evidence is accepted and
   [COMPATIBILITY.md](COMPATIBILITY.md) states no stronger claim;
6. final `.deb`, checksum, attestation, offline bundle, release notes, dependency
   inventory, and payload ledger reconcile exactly;
7. the publisher/repository identity and current GitHub immutable-release state
   have been reviewed without assuming a setting change; and
8. Cody separately authorizes the exact GitHub publication after seeing the
   immutable candidate evidence.

Passing source tests, contract acceptance, an implemented package, a physical
report, or a signed attestation alone is insufficient.

## Authorization boundaries

Acceptance of this document authorizes no implementation and no external
mutation. Package implementation, physical validation, release-candidate
creation, publisher or legal identity, GitHub repository settings, signing or
attestation permissions, uploads, tags, releases, issue submissions, APT or PPA
creation, distro submissions, store submissions, and public compatibility
claims each remain behind their exact later authority.

This phase does not install software, obtain paid services, enroll accounts,
change device permissions, add users to groups, enable hardware or optional OS
features, publish artifacts, or create a successor task.

## Requirement disposition

| Requirement | Contract owner or exclusion |
|---|---|
| Supported architecture/distribution | Ubuntu 24.04 LTS `amd64` only; all others unclaimed |
| Native artifact | One planned system `.deb` with required copyright/notices, packaging changelog, and `inlaid(1)` man page; no generic portable binary |
| Discovery/channel | Direct immutable GitHub Release; repository channels deferred |
| Command and install location | Direct `/usr/bin/inlaid`; fresh native shell; no wrapper or PATH edit |
| System/user privilege | System package transaction; unprivileged runtime; no user installer |
| Channel/package collisions | `inlaid-terminal-webcam` owns `/usr/bin/inlaid`; fail closed on foreign/unowned/diverted paths; dpkg cannot distinguish publishers reusing the package name; preserve user-PATH shadows |
| Install/repair | Exact local APT install plus same-artifact `--reinstall`; inspect actual dpkg/filesystem state; no app repair mode |
| Upgrade/downgrade/failure | Manual newer `.deb`, explicit verified `--allow-downgrades`, same package identity for any future project repository, and evidence-based recovery rather than promised rollback |
| Uninstall ownership | Package-owned system files only; remove and purge preserve every home/XDG path |
| XDG data | Explicit config/data/state/cache homes plus non-sourced absolute or `$HOME` user-dir forms and safe fallbacks |
| Source/manual behavior | Verified source routes remain experimental; explicit source root; no binary tarball |
| Migration | Explicit copy-only source migration and tested marked-portable import; no scanning or deletion |
| V4L2/libturbojpeg | Direct V4L2/MMAP and native MJPEG; derived runtime library dependency; build tools excluded from runtime |
| FFmpeg/desktop helpers | FFmpeg `Suggests`, xdg-utils `Recommends`; both degrade without blocking preview/PNG |
| Permissions | Host owns camera access; private new state/report paths; package never changes groups or udev |
| Runtime/payload seams | Linux XDG adapter and Linux destination profile; Windows results must remain unchanged |
| Version identity | Exact executable tag plus deterministic Debian mapping and unique package revision |
| Signing/checksums/provenance | SHA-256 plus repository/workflow/ref/digest-bound GitHub attestation; offline bundle and trusted root; resolved runner image recorded |
| Native versus physical evidence | Exact commit/run baseline is necessary but not package or hardware evidence; physical community report gates positive hardware/support claims and publication, not bounded source classifications |
| Publication prerequisites | Eight-part gate above; no single evidence class is sufficient |
| External actions | Every implementation, account, setting, signing, upload, submission, and claim is separately gated |

[debian-files]: https://www.debian.org/doc/debian-policy/ch-files.html
[debian-copyright]: https://www.debian.org/doc/debian-policy/ch-source.html#copyright-debian-copyright
[debian-documentation]: https://www.debian.org/doc/debian-policy/ch-docs.html
[debian-maintainer-scripts]: https://www.debian.org/doc/debian-policy/ch-maintainerscripts.html
[debian-os]: https://www.debian.org/doc/debian-policy/ch-opersys.html
[debian-shared-libraries]: https://www.debian.org/doc/debian-policy/ch-sharedlibs.html
[debian-version]: https://www.debian.org/doc/debian-policy/ch-controlfields.html#version
[dpkg-divert]: https://manpages.debian.org/trixie/dpkg/dpkg-divert.1.en.html
[dpkg-query]: https://manpages.debian.org/trixie/dpkg/dpkg-query.1.en.html
[github-attestations]: https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations
[github-cli-attestation]: https://cli.github.com/manual/gh_attestation_verify
[github-cli-trusted-root]: https://cli.github.com/manual/gh_attestation_trusted-root
[github-immutable-releases]: https://docs.github.com/en/enterprise-cloud@latest/code-security/concepts/supply-chain-security/immutable-releases
[github-releases]: https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases
[ubuntu-apt]: https://manpages.ubuntu.com/manpages/noble/man8/apt.8.html
[ubuntu-ffmpeg]: https://packages.ubuntu.com/noble/ffmpeg
[ubuntu-release-cycle]: https://ubuntu.com/about/release-cycle
[ubuntu-security-coverage]: https://documentation.ubuntu.com/security/security-updates/
[ubuntu-turbojpeg]: https://packages.ubuntu.com/noble/libturbojpeg
[ubuntu-xdg-utils]: https://packages.ubuntu.com/noble/xdg-utils
[v4l2-capture]: https://www.kernel.org/doc/html/latest/userspace-api/media/v4l/dev-capture.html
[v4l2-mmap]: https://www.kernel.org/doc/html/latest/userspace-api/media/v4l/mmap.html
[xdg-base]: https://specifications.freedesktop.org/basedir-spec/latest/
[xdg-user-dirs]: https://www.freedesktop.org/wiki/Software/xdg-user-dirs/
