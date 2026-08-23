[CmdletBinding()]
param(
    [string]$Version = 'dev',
    [string]$Executable = '',
    [string]$OutputDirectory = ''
)

$ErrorActionPreference = 'Stop'
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

if ([string]::IsNullOrWhiteSpace($Executable)) {
    $Executable = Join-Path $ProjectRoot 'bin\inlaid.exe'
}
elseif (-not [System.IO.Path]::IsPathRooted($Executable)) {
    $Executable = Join-Path $ProjectRoot $Executable
}

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $ProjectRoot 'dist'
}
elseif (-not [System.IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $ProjectRoot $OutputDirectory
}

$Version = $Version.Trim()
if ($Version -notmatch '^[0-9A-Za-z][0-9A-Za-z._-]*$') {
    throw "Version contains unsupported filename characters: '$Version'"
}
if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
    throw "Release executable was not found: $Executable"
}

$packageFiles = @(
    'START-INLAID.cmd',
    'START-INLAID.ps1',
    'README.md',
    'CHANGELOG.md',
    'CONTRIBUTING.md',
    'LICENSE',
    'SECURITY.md',
    'THIRD_PARTY_NOTICES.md',
    'docs\CELL_PIPELINE.md',
    'docs\DESIGN.md',
    'docs\FILTERS.md',
    'filters\README.md',
    'scripts\install-ffmpeg.ps1'
)
foreach ($relative in $packageFiles) {
    $source = Join-Path $ProjectRoot $relative
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
        throw "Release file was not found: $source"
    }
}

$artifactName = "inlaid-$Version-windows-amd64"
$archivePath = Join-Path $OutputDirectory "$artifactName.zip"
$checksumPath = Join-Path $OutputDirectory 'SHA256SUMS.txt'
$temporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) (
    'inlaid-release-' + [Guid]::NewGuid().ToString('N')
)
$packageRoot = Join-Path $temporaryRoot $artifactName

try {
    New-Item -ItemType Directory -Force -Path (Join-Path $packageRoot 'bin') | Out-Null
    Copy-Item -LiteralPath $Executable -Destination (Join-Path $packageRoot 'bin\inlaid.exe')
    # Keep this a file-by-file allowlist. In particular, never copy the filters
    # directory recursively: it may contain a user's private .cube files even
    # though those files are ignored by Git.
    foreach ($relative in $packageFiles) {
        $destination = Join-Path $packageRoot $relative
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
        Copy-Item -LiteralPath (Join-Path $ProjectRoot $relative) -Destination $destination
    }

    # FFmpeg is GPL software distributed by its own publishers. Inlaid
    # discovers a user-installed copy or the local copy fetched by setup.ps1;
    # release archives intentionally never redistribute it.
    $bundledFFmpeg = Get-ChildItem -LiteralPath $packageRoot -Filter 'ffmpeg*.exe' -File -Recurse |
        Select-Object -First 1
    if ($null -ne $bundledFFmpeg) {
        throw "Refusing to package bundled FFmpeg: $($bundledFFmpeg.FullName)"
    }

    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
    Compress-Archive -LiteralPath $packageRoot -DestinationPath $archivePath -CompressionLevel Optimal -Force
    $hash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $(Split-Path -Leaf $archivePath)" | Set-Content -LiteralPath $checksumPath -Encoding ascii
}
finally {
    if (Test-Path -LiteralPath $temporaryRoot -PathType Container) {
        $resolvedTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
        $resolvedPackage = [System.IO.Path]::GetFullPath($temporaryRoot)
        if ($resolvedPackage.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $resolvedPackage -Recurse -Force
        }
    }
}

Write-Host "Created: $archivePath" -ForegroundColor Green
Write-Host "Checksum: $checksumPath" -ForegroundColor Green
