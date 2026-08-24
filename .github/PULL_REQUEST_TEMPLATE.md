## What changed


## Why


## Evidence

- Checks run:
- User-visible result:
- Before/after capture, when useful:

| Environment | Details |
|---|---|
| Version or commit | |
| OS and architecture | |
| Camera and actual mode | |
| Terminal, shell, and launch method | |
| Duration, FPS, and memory behavior | |
| PNG, MP4, GIF, close, and reopen | |

Delete environment rows that cannot be affected by the change. Do not claim hardware support from a cross-build or CI job.

## Risk

- Failure mode:
- Recovery or rollback:

## Checklist

- [ ] I kept preview, snapshots, recordings, and recovery on the same canonical cell representation.
- [ ] I did not add unbounded queues or work to the live frame path without measurements.
- [ ] I treated camera metadata, settings, looks, recovery data, terminal input, and executable paths as untrusted where applicable.
- [ ] I updated public behavior, compatibility, tests, notices, or the changelog where needed.
- [ ] I removed private camera media, machine-specific paths, identifiers, and credentials from this pull request.
