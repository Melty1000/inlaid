[CmdletBinding()]
param(
    [string]$Version = 'ci',
    [string]$Executable = 'bin\inlaid.exe'
)

$ErrorActionPreference = 'Stop'
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$TemporaryBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$TemporaryRoot = Join-Path $TemporaryBase ('inlaid-package-test-' + [Guid]::NewGuid().ToString('N'))
$TemporaryRoot = [System.IO.Path]::GetFullPath($TemporaryRoot)
if (-not $TemporaryRoot.StartsWith($TemporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'Package test directory escaped the system temporary directory.'
}

try {
    $OutputDirectory = Join-Path $TemporaryRoot 'dist'
    $ExpandedDirectory = Join-Path $TemporaryRoot 'expanded'
    & (Join-Path $PSScriptRoot 'package-release.ps1') `
        -Version $Version `
        -Executable $Executable `
        -OutputDirectory $OutputDirectory

    $ArchiveName = "inlaid-$Version-windows-amd64.zip"
    $ArchivePath = Join-Path $OutputDirectory $ArchiveName
    $ChecksumPath = Join-Path $OutputDirectory 'SHA256SUMS.txt'
    if (-not (Test-Path -LiteralPath $ArchivePath -PathType Leaf) -or
        -not (Test-Path -LiteralPath $ChecksumPath -PathType Leaf)) {
        throw 'Release package or checksum was not created.'
    }

    $ExpectedHash = ((Get-Content -LiteralPath $ChecksumPath -Raw).Trim() -split '\s+')[0]
    $ActualHash = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($ExpectedHash -ne $ActualHash) {
        throw "Release checksum mismatch: $ExpectedHash != $ActualHash"
    }

    Expand-Archive -LiteralPath $ArchivePath -DestinationPath $ExpandedDirectory
    $PackageRoot = Join-Path $ExpandedDirectory "inlaid-$Version-windows-amd64"
    foreach ($RelativePath in @(
            'bin\inlaid.exe',
            'START-INLAID.cmd',
            'START-INLAID.ps1',
            'README.md',
            'CHANGELOG.md',
            'LICENSE',
            'SECURITY.md',
            'THIRD_PARTY_NOTICES.md'
        )) {
        if (-not (Test-Path -LiteralPath (Join-Path $PackageRoot $RelativePath) -PathType Leaf)) {
            throw "Release package is missing $RelativePath."
        }
    }

    $ForbiddenFile = Get-ChildItem -LiteralPath $PackageRoot -File -Recurse |
        Where-Object {
            $_.Name -like 'ffmpeg*.exe' -or
            $_.Extension -ieq '.cube' -or
            $_.Name -in @('inlaid-settings.json', 'webcam-settings.json')
        } |
        Select-Object -First 1
    if ($null -ne $ForbiddenFile) {
        throw "Private or redistributable file entered the package: $($ForbiddenFile.FullName)"
    }
    foreach ($ForbiddenDirectory in @('recordings', 'snapshots', 'support-reports', '.tools')) {
        if (Test-Path -LiteralPath (Join-Path $PackageRoot $ForbiddenDirectory)) {
            throw "Private or generated directory entered the package: $ForbiddenDirectory"
        }
    }

    $PackagedExecutable = Join-Path $PackageRoot 'bin\inlaid.exe'
    $VersionText = & $PackagedExecutable --version
    if ($LASTEXITCODE -ne 0 -or $VersionText -notmatch '^Inlaid\s+\S+$') {
        throw "Packaged executable did not report a version: $VersionText"
    }
    $Preview = & $PackagedExecutable --render-preview 80x24
    if ($LASTEXITCODE -ne 0 -or ($Preview -join "`n") -notmatch 'INLAID') {
        throw 'Packaged executable did not render the deterministic 80x24 preview.'
    }
}
finally {
    if (Test-Path -LiteralPath $TemporaryRoot -PathType Container) {
        $ResolvedTemporaryRoot = [System.IO.Path]::GetFullPath($TemporaryRoot)
        if ($ResolvedTemporaryRoot.StartsWith($TemporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $ResolvedTemporaryRoot -Recurse -Force
        }
    }
}

Write-Host 'Release package smoke test passed.' -ForegroundColor Green
