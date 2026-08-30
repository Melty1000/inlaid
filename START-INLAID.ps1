[CmdletBinding()]
param(
    [switch]$Rebuild
)

$ErrorActionPreference = 'Stop'
$ProjectRoot = $PSScriptRoot
$SourcePackage = Join-Path $ProjectRoot 'cmd\inlaid'
$Executable = Join-Path $ProjectRoot 'bin\inlaid.exe'
$SetupScript = Join-Path $ProjectRoot 'scripts\setup.ps1'
$MediaInstaller = Join-Path $ProjectRoot 'scripts\install-ffmpeg.ps1'
$WindowTitle = 'Inlaid'

function Resolve-GoExecutable {
    $localGo = Join-Path $ProjectRoot '.tools\go\bin\go.exe'
    if (Test-Path -LiteralPath $localGo -PathType Leaf) {
        return $localGo
    }

    $installedGo = Get-Command -Name 'go.exe' -CommandType Application -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -ne $installedGo) {
        return $installedGo.Source
    }

    throw 'This source checkout needs Go 1.26 or newer. Install Go from https://go.dev/dl/, or download a prebuilt Inlaid release.'
}

function Test-BuildRequired {
    if ($Rebuild -or -not (Test-Path -LiteralPath $Executable -PathType Leaf)) {
        return $true
    }
    if (-not (Test-Path -LiteralPath $SourcePackage -PathType Container)) {
        return $false
    }

    $builtAt = (Get-Item -LiteralPath $Executable).LastWriteTimeUtc
    $inputs = @(
        Get-Item -LiteralPath (Join-Path $ProjectRoot 'go.mod'), (Join-Path $ProjectRoot 'go.sum')
        Get-ChildItem -LiteralPath $SourcePackage -Filter '*.go' -File -Recurse
        Get-ChildItem -LiteralPath (Join-Path $ProjectRoot 'internal') -Filter '*.go' -File -Recurse
    )
    return $null -ne ($inputs |
        Where-Object { $_.LastWriteTimeUtc -gt $builtAt } |
        Select-Object -First 1)
}

function Build-Inlaid {
    if (-not (Test-Path -LiteralPath $SourcePackage -PathType Container)) {
        throw 'inlaid.exe is missing from this release. Download the Windows release again.'
    }

    $go = Resolve-GoExecutable
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Executable) | Out-Null

    Write-Host 'Building Inlaid...' -ForegroundColor Cyan
    Push-Location -LiteralPath $ProjectRoot
    try {
        & $go build -trimpath -o $Executable '.\cmd\inlaid'
        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed with exit code $LASTEXITCODE. Run .\scripts\setup.ps1 for a full setup check."
        }
    }
    finally {
        Pop-Location
    }
}

function Test-FFmpegAvailable {
    $configured = [Environment]::GetEnvironmentVariable('INLAID_FFMPEG')
    if (-not [string]::IsNullOrWhiteSpace($configured) -and
        (Test-Path -LiteralPath $configured -PathType Leaf)) {
        return $true
    }
    if (Test-Path -LiteralPath (Join-Path $ProjectRoot '.tools\ffmpeg\bin\ffmpeg.exe') -PathType Leaf) {
        return $true
    }
    return $null -ne (Get-Command -Name 'ffmpeg.exe' -CommandType Application -ErrorAction SilentlyContinue |
        Select-Object -First 1)
}

try {
    try {
        $Host.UI.RawUI.WindowTitle = $WindowTitle
    }
    catch {
        # An unusual host may reject title changes; that should not block video.
    }

    if (Test-BuildRequired) {
        if (-not (Test-Path -LiteralPath $Executable -PathType Leaf) -and
            (Test-Path -LiteralPath $SetupScript -PathType Leaf)) {
            & $SetupScript
            if ($LASTEXITCODE -ne 0) {
                throw "Setup failed with exit code $LASTEXITCODE."
            }
        }
        else {
            Build-Inlaid
        }
    }

    if (-not (Test-FFmpegAvailable) -and (Test-Path -LiteralPath $MediaInstaller -PathType Leaf)) {
        try {
            & $MediaInstaller | Out-Null
        }
        catch {
            Write-Host 'Video export is not ready yet; live preview and snapshots will still work.' -ForegroundColor Yellow
            Write-Host $_.Exception.Message -ForegroundColor DarkGray
            Start-Sleep -Milliseconds 900
        }
    }

    & $Executable '--source-root' $ProjectRoot
    $appExitCode = $LASTEXITCODE
    if ($appExitCode -ne 0) {
        throw "Inlaid stopped unexpectedly with exit code $appExitCode."
    }
    exit 0
}
catch {
    Write-Host ''
    Write-Host 'Inlaid could not start.' -ForegroundColor Red
    Write-Host $_.Exception.Message -ForegroundColor Red
    Write-Host ''
    [void](Read-Host 'Press Enter to close')
    exit 1
}
