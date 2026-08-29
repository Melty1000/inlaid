[CmdletBinding()]
param(
    [string]$Version = 'dev',
    [string]$Executable = '',
    [string]$OutputDirectory = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'resolve-payload.ps1')
. (Join-Path $PSScriptRoot 'assert-amd64-pe.ps1')

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
if ($Version -notmatch '^v[0-9][0-9A-Za-z._-]*$') {
    throw "Version must be a leading-v executable identity with safe filename characters: '$Version'"
}
if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
    throw "Release executable was not found: $Executable"
}

$null = Assert-InlaidAmd64Pe32Plus -Path $Executable -Description 'Portable application payload'
$identity = @(& $Executable --version)
if ($LASTEXITCODE -ne 0 -or $identity.Count -ne 1 -or $identity[0] -cne "Inlaid $Version") {
    throw "Release executable identity must be exactly 'Inlaid $Version'; got '$($identity -join ' ')'."
}
$packageFiles = Resolve-InlaidPayload -ProjectRoot $ProjectRoot -Platform windows -Profile portable -Executable $Executable

$artifactName = "inlaid-$Version-windows-amd64"
$archivePath = Join-Path $OutputDirectory "$artifactName.zip"
$checksumPath = Join-Path $OutputDirectory 'SHA256SUMS.txt'
$temporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) (
    'inlaid-release-' + [Guid]::NewGuid().ToString('N')
)
$packageRoot = Join-Path $temporaryRoot $artifactName

try {
    # The manifest is a file-by-file allowlist. In particular, never copy the
    # filters directory recursively: it may contain private .cube files.
    foreach ($entry in $packageFiles | Where-Object { $_.SourceToken -ne '@generated-portable-manifest' }) {
        $destination = Join-Path $packageRoot $entry.Destination
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
        Copy-Item -LiteralPath $entry.Source -Destination $destination
    }

    $portableManifestEntries = @($packageFiles | Where-Object { $_.SourceToken -eq '@generated-portable-manifest' })
    if ($portableManifestEntries.Count -ne 1) { throw 'Portable profile must have exactly one generated portable manifest role.' }
    $portableManifestEntry = $portableManifestEntries[0]
    $ownedFiles = @($packageFiles |
        Where-Object { $_.SourceToken -ne '@generated-portable-manifest' } |
        ForEach-Object {
            $path = Join-Path $packageRoot $_.Destination
            [ordered]@{
                role = $_.Role
                path = $_.Destination.Replace('\', '/')
                sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
            }
        })
    $portableManifest = [ordered]@{
        schema = 2
        layout = 'portable'
        version = $Version
        files = $ownedFiles
    }
    $portableManifestPath = Join-Path $packageRoot $portableManifestEntry.Destination
    $portableManifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $portableManifestPath -Encoding utf8

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
