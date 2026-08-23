[CmdletBinding()]
param([switch]$Force)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$ToolsRoot = Join-Path $ProjectRoot '.tools'
$DownloadRoot = Join-Path $ToolsRoot 'downloads'
$FFmpegVersion = '9.0.1'
$ArchiveName = "ffmpeg-$FFmpegVersion-essentials_build.zip"
$ArchiveURL = "https://www.gyan.dev/ffmpeg/builds/packages/$ArchiveName"
$ArchiveSHA256 = 'fec81ae03971d9dd4be3ebe02e263bd2ec1d789483f931bdba5f5715e65da2e9'
$LocalExecutable = Join-Path $ToolsRoot 'ffmpeg\bin\ffmpeg.exe'

function Test-FFmpegExecutable {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return $false
    }
    $reported = (& $Path -hide_banner -version 2>&1 | Select-Object -First 1 | Out-String).Trim()
    # Some PowerShell hosts do not preserve LASTEXITCODE through this short
    # pipeline. The version banner is the stable cross-host startup check.
    return $reported -match '^ffmpeg version\s+'
}

if (-not $Force -and (Test-FFmpegExecutable -Path $LocalExecutable)) {
    Write-Output $LocalExecutable
    exit 0
}

if (-not $Force) {
    $installed = Get-Command -Name 'ffmpeg.exe' -CommandType Application -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -ne $installed -and (Test-FFmpegExecutable -Path $installed.Source)) {
        Write-Output $installed.Source
        exit 0
    }
}

New-Item -ItemType Directory -Force -Path $DownloadRoot | Out-Null
$archive = Join-Path $DownloadRoot $ArchiveName
$archiveReady = $false
if (-not $Force -and (Test-Path -LiteralPath $archive -PathType Leaf)) {
    $archiveReady = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant() -eq $ArchiveSHA256
}

if (-not $archiveReady) {
    Write-Host "Downloading FFmpeg $FFmpegVersion for MP4 and GIF export (one time)..." -ForegroundColor Cyan
    $download = Join-Path ([System.IO.Path]::GetTempPath()) (
        'inlaid-ffmpeg-' + [Guid]::NewGuid().ToString('N') + '.zip'
    )
    try {
        Invoke-WebRequest -Uri $ArchiveURL -OutFile $download -UseBasicParsing
        $downloadHash = (Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($downloadHash -ne $ArchiveSHA256) {
            throw "Downloaded FFmpeg checksum mismatch. Expected $ArchiveSHA256, got $downloadHash."
        }
        Copy-Item -LiteralPath $download -Destination $archive -Force
    }
    finally {
        if ([System.IO.File]::Exists($download)) {
            [System.IO.File]::Delete($download)
        }
    }
}

$extractRoot = Join-Path ([System.IO.Path]::GetTempPath()) (
    'inlaid-ffmpeg-' + [Guid]::NewGuid().ToString('N')
)
try {
    New-Item -ItemType Directory -Force -Path $extractRoot | Out-Null
    Write-Host 'Preparing video export...' -ForegroundColor Cyan
    Expand-Archive -LiteralPath $archive -DestinationPath $extractRoot -Force
    $candidate = Get-ChildItem -LiteralPath $extractRoot -Filter 'ffmpeg.exe' -File -Recurse |
        Where-Object { $_.Directory.Name -eq 'bin' } |
        Select-Object -First 1
    if ($null -eq $candidate) {
        throw 'The verified FFmpeg archive did not contain bin\ffmpeg.exe.'
    }

    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $LocalExecutable) | Out-Null
    Copy-Item -LiteralPath $candidate.FullName -Destination $LocalExecutable -Force
}
finally {
    if (Test-Path -LiteralPath $extractRoot -PathType Container) {
        $resolvedTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
        $resolvedExtract = [System.IO.Path]::GetFullPath($extractRoot)
        if (-not $resolvedExtract.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to clean an unexpected extraction path: $resolvedExtract"
        }
        Remove-Item -LiteralPath $resolvedExtract -Recurse -Force
    }
}

if (-not (Test-FFmpegExecutable -Path $LocalExecutable)) {
    throw 'FFmpeg was installed but did not pass its startup check.'
}

Write-Host 'Video export is ready.' -ForegroundColor Green
Write-Output $LocalExecutable
