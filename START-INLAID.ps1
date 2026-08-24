[CmdletBinding()]
param(
    [switch]$Rebuild
)

$ErrorActionPreference = 'Stop'
$ProjectRoot = $PSScriptRoot
$SourcePackage = Join-Path $ProjectRoot 'cmd\inlaid'
$Executable = Join-Path $ProjectRoot 'bin\inlaid.exe'
$SettingsPath = Join-Path $ProjectRoot 'inlaid-settings.json'
$SetupScript = Join-Path $ProjectRoot 'scripts\setup.ps1'
$MediaInstaller = Join-Path $ProjectRoot 'scripts\install-ffmpeg.ps1'
$WindowTitle = 'Inlaid'

function ConvertTo-WindowsProcessArgument {
    param([AllowEmptyString()][string]$Argument)

    if ($Argument.Length -gt 0 -and $Argument -notmatch '[\s"]') {
        return $Argument
    }

    $builder = [System.Text.StringBuilder]::new()
    [void]$builder.Append('"')
    $backslashes = 0
    foreach ($character in $Argument.ToCharArray()) {
        if ($character -eq '\') {
            $backslashes++
            continue
        }
        if ($character -eq '"') {
            [void]$builder.Append(('\' * (($backslashes * 2) + 1)))
            [void]$builder.Append('"')
            $backslashes = 0
            continue
        }
        if ($backslashes -gt 0) {
            [void]$builder.Append(('\' * $backslashes))
            $backslashes = 0
        }
        [void]$builder.Append($character)
    }
    if ($backslashes -gt 0) {
        [void]$builder.Append(('\' * ($backslashes * 2)))
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

function New-SafeProcessStartInfo {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true

    if ($null -ne $startInfo.PSObject.Properties['ArgumentList']) {
        foreach ($argument in $Arguments) {
            [void]$startInfo.ArgumentList.Add($argument)
        }
    }
    else {
        $startInfo.Arguments = (($Arguments | ForEach-Object {
                    ConvertTo-WindowsProcessArgument -Argument $_
                }) -join ' ')
    }
    return $startInfo
}

function Test-WindowsTerminalHost {
    return -not [string]::IsNullOrWhiteSpace($env:WT_SESSION)
}

function Start-WindowsTerminalHost {
    if (Test-WindowsTerminalHost) {
        return $false
    }

    $terminal = Get-Command -Name 'wt.exe' -CommandType Application -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -eq $terminal) {
        throw 'Windows Terminal (wt.exe) was not found. Install Windows Terminal, then double-click START-INLAID.cmd again.'
    }

    $terminalArguments = @(
        '-w', 'new',
        'new-tab',
        '-d', $ProjectRoot,
        '--',
        $Executable,
        '--settings', $SettingsPath
    )

    $startInfo = New-SafeProcessStartInfo -FilePath $terminal.Source -Arguments $terminalArguments
    $startInfo.EnvironmentVariables['INLAID_LAUNCHER'] = $(if (Test-Path -LiteralPath $SourcePackage -PathType Container) { 'source' } else { 'package' })

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) {
            throw 'Windows Terminal could not be started.'
        }
    }
    finally {
        $process.Dispose()
    }
    return $true
}

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

    if (Start-WindowsTerminalHost) {
        exit 0
    }

    Push-Location -LiteralPath $ProjectRoot
    try {
        & $Executable '--settings' $SettingsPath
        $appExitCode = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
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
