[CmdletBinding()]
param(
    [switch]$Check,
    [switch]$ForceFFmpeg
)

$ErrorActionPreference = 'Stop'
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$ToolsRoot = Join-Path $ProjectRoot '.tools'
$BinRoot = Join-Path $ProjectRoot 'bin'
$Executable = Join-Path $BinRoot 'inlaid.exe'
$MediaInstaller = Join-Path $PSScriptRoot 'install-ffmpeg.ps1'

function Resolve-GoExecutable {
    $localGo = Join-Path $ToolsRoot 'go\bin\go.exe'
    if (Test-Path -LiteralPath $localGo -PathType Leaf) {
        return $localGo
    }

    $installedGo = Get-Command -Name 'go.exe' -CommandType Application -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -ne $installedGo) {
        return $installedGo.Source
    }

    throw @'
Go 1.26 or newer was not found.

Install Go from https://go.dev/dl/, open a new PowerShell window, and run:
  .\scripts\setup.ps1

The project also accepts a portable Go toolchain at .tools\go\bin\go.exe.
'@
}

function Assert-GoVersion {
    param([Parameter(Mandatory)][string]$GoExecutable)

    $reported = (& $GoExecutable version 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "Go could not start: $reported"
    }
    if ($reported -match '\bgo(?<major>\d+)\.(?<minor>\d+)') {
        $major = [int]$Matches.major
        $minor = [int]$Matches.minor
        if ($major -lt 1 -or ($major -eq 1 -and $minor -lt 26)) {
            throw "Inlaid needs Go 1.26 or newer. Found: $reported"
        }
    }
    Write-Host "Using $reported" -ForegroundColor DarkGray
}

$go = Resolve-GoExecutable
Assert-GoVersion -GoExecutable $go
$ffmpeg = & $MediaInstaller -Force:$ForceFFmpeg | Select-Object -Last 1

New-Item -ItemType Directory -Force -Path $BinRoot | Out-Null
Push-Location -LiteralPath $ProjectRoot
try {
    Write-Host 'Downloading Go modules...' -ForegroundColor Cyan
    & $go mod download
    if ($LASTEXITCODE -ne 0) {
        throw "go mod download failed with exit code $LASTEXITCODE."
    }

    if ($Check) {
        Write-Host 'Running tests...' -ForegroundColor Cyan
        & $go test ./...
        if ($LASTEXITCODE -ne 0) {
            throw "go test failed with exit code $LASTEXITCODE."
        }

        Write-Host 'Running static checks...' -ForegroundColor Cyan
        & $go vet ./...
        if ($LASTEXITCODE -ne 0) {
            throw "go vet failed with exit code $LASTEXITCODE."
        }
    }

    Write-Host 'Building Inlaid...' -ForegroundColor Cyan
    & $go build -trimpath -o $Executable '.\cmd\inlaid'
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed with exit code $LASTEXITCODE."
    }
}
finally {
    Pop-Location
}

Write-Host ''
Write-Host "Ready: $Executable" -ForegroundColor Green
Write-Host "FFmpeg: $ffmpeg" -ForegroundColor DarkGray
Write-Host 'Next: double-click START-INLAID.cmd.'
