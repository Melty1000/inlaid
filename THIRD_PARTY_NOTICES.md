# Third-party notices

This file covers the Go code linked into the Windows executable for `v0.1.0-beta.1` and the external FFmpeg executable used for MP4/GIF export.

Inlaid itself is licensed under the MIT License in [LICENSE](LICENSE).

## Linked Go software

The Windows executable contains the Go runtime and code from these modules:

| Module | Version | License / copyright notice |
|---|---|---|
| [charm.land/bubbles/v2](https://github.com/charmbracelet/bubbles) | 2.1.1 | MIT; © 2020–2026 Charmbracelet, Inc. |
| [charm.land/bubbletea/v2](https://github.com/charmbracelet/bubbletea) | 2.0.8 | MIT; © 2020–2026 Charmbracelet, Inc. |
| [charm.land/lipgloss/v2](https://github.com/charmbracelet/lipgloss) | 2.0.6 | MIT; © 2021–2026 Charmbracelet, Inc. |
| [github.com/charmbracelet/colorprofile](https://github.com/charmbracelet/colorprofile) | 0.4.3 | MIT; © 2020–2024 Charmbracelet, Inc. |
| [github.com/charmbracelet/ultraviolet](https://github.com/charmbracelet/ultraviolet) | 0.0.0-20260811164956-006e29f97886 | MIT; © 2025 Charmbracelet, Inc. |
| [github.com/charmbracelet/x/ansi](https://github.com/charmbracelet/x/tree/main/ansi) | 0.11.8 | MIT; © 2023 Charmbracelet, Inc. |
| [github.com/charmbracelet/x/mosaic](https://github.com/charmbracelet/x/tree/main/mosaic) | 0.0.0-20260816001655-68d539dca504 | MIT; © 2023 Charmbracelet, Inc. |
| [github.com/charmbracelet/x/term](https://github.com/charmbracelet/x/tree/main/term) | 0.2.2 | MIT; © 2023 Charmbracelet, Inc. |
| [github.com/charmbracelet/x/windows](https://github.com/charmbracelet/x/tree/main/windows) | 0.2.2 | MIT; © 2023 Charmbracelet, Inc. |
| [github.com/clipperhouse/displaywidth](https://github.com/clipperhouse/displaywidth) | 0.11.0 | MIT; © 2025 Matt Sherman |
| [github.com/clipperhouse/uax29/v2](https://github.com/clipperhouse/uax29) | 2.7.0 | MIT; © 2020 Matt Sherman |
| [github.com/lucasb-eyer/go-colorful](https://github.com/lucasb-eyer/go-colorful) | 1.4.1 | MIT; © 2013 Lucas Beyer |
| [github.com/mattn/go-runewidth](https://github.com/mattn/go-runewidth) | 0.0.24 | MIT; © 2016 Yasuhiro Matsumoto |
| [github.com/muesli/cancelreader](https://github.com/muesli/cancelreader) | 0.2.2 | MIT; © 2022 Erik Geiser and Christian Muehlhaeuser |
| [github.com/rivo/uniseg](https://github.com/rivo/uniseg) | 0.4.7 | MIT; © 2019 Oliver Kuederle |
| [github.com/xo/terminfo](https://github.com/xo/terminfo) | 0.0.0-20220910002029-abceb7e1c41e | MIT; © 2016 Anmol Sethi |
| [golang.org/x/image](https://pkg.go.dev/golang.org/x/image) | 0.45.0 | BSD-3-Clause; © 2009 The Go Authors |
| [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync) | 0.22.0 | BSD-3-Clause; © 2009 The Go Authors |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | 0.47.0 | BSD-3-Clause; © 2009 The Go Authors |
| [Go runtime and standard library](https://go.dev/) | compiler-defined | BSD-3-Clause; © 2009 The Go Authors |

The exact module graph for a source checkout is recorded in `go.mod` and `go.sum`. The launcher and setup script do not download a Go toolchain; source builds use Go already installed by the user or already present at `.tools\go\bin\go.exe`.

## External FFmpeg

FFmpeg is **not bundled** in the source repository, Inlaid executable, or release ZIP.

The packaged `scripts\install-ffmpeg.ps1` first accepts a working user-installed FFmpeg. If none is found, the launcher automatically attempts one download of `ffmpeg-9.0.1-essentials_build.zip` from [gyan.dev](https://www.gyan.dev/ffmpeg/builds/) and verifies SHA-256 `fec81ae03971d9dd4be3ebe02e263bd2ec1d789483f931bdba5f5715e65da2e9` before copying `ffmpeg.exe` into the local `.tools` directory.

That Gyan Essentials build reports `--enable-gpl --enable-version3` and includes libraries such as x264. It is separately distributed GPL-enabled software, not a derivative component linked into Inlaid. Its license files, build configuration, and corresponding-source information are provided by the FFmpeg and Gyan projects:

- [FFmpeg legal and license information](https://ffmpeg.org/legal.html)
- [FFmpeg source code](https://ffmpeg.org/download.html#get-sources)
- [Gyan Windows builds](https://www.gyan.dev/ffmpeg/builds/)

Users may instead set `INLAID_FFMPEG` or place another compatible `ffmpeg.exe` on `PATH`. The license of that chosen build depends on how it was configured.

## MIT License text

The MIT-licensed components above are provided under this license, with their respective copyright notices retained in the table:

> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in all
> copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.

## BSD 3-Clause License text

The Go runtime, standard library, and `golang.org/x` components above are provided under this license:

> Copyright 2009 The Go Authors.
>
> Redistribution and use in source and binary forms, with or without
> modification, are permitted provided that the following conditions are met:
>
> * Redistributions of source code must retain the above copyright notice,
>   this list of conditions and the following disclaimer.
> * Redistributions in binary form must reproduce the above copyright notice,
>   this list of conditions and the following disclaimer in the documentation
>   and/or other materials provided with the distribution.
> * Neither the name of Google LLC nor the names of its contributors may be used
>   to endorse or promote products derived from this software without specific
>   prior written permission.
>
> THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
> AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
> IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
> ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE
> LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
> CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
> SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
> INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
> CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
> ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
> POSSIBILITY OF SUCH DAMAGE.
