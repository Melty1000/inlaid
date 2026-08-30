[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$MsiVersion,
    [string]$Executable = '',
    [string]$OutputDirectory = '',
    [string]$Wix = 'wix',
    [switch]$TestBuild,
    [switch]$EnableTestHooks
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'resolve-payload.ps1')
. (Join-Path $PSScriptRoot 'assert-amd64-pe.ps1')

function Assert-PathHelperTestHookMode([string]$Path, [bool]$Enabled, [string]$ProbeStatePath) {
    foreach ($candidate in @($ProbeStatePath, "$ProbeStatePath.partial", "$ProbeStatePath.claim", "$ProbeStatePath.claim.partial")) {
        if (Test-Path -LiteralPath $candidate) { throw "PATH-helper mode probe state already exists: $candidate" }
    }
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $Path
    $start.Arguments = '--action fail --state-file "' + $ProbeStatePath.Replace('"', '\"') + '"'
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $start
    try {
        if (-not $process.Start()) { throw 'PATH-helper mode probe process did not start.' }
        $stdout = $process.StandardOutput.ReadToEnd()
        $stderr = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        $exitCode = $process.ExitCode
    }
    finally { $process.Dispose() }
    foreach ($candidate in @($ProbeStatePath, "$ProbeStatePath.partial", "$ProbeStatePath.claim", "$ProbeStatePath.claim.partial")) {
        if (Test-Path -LiteralPath $candidate) { throw "PATH-helper mode probe unexpectedly created transaction state: $candidate" }
    }
    $failureText = 'Inlaid PATH helper: injected failure after PATH mutation' + "`n"
    if ($Enabled) {
        if ($exitCode -ne 1 -or $stdout -cne '' -or $stderr -cne $failureText) {
            throw "Enabled PATH-helper test-hook probe mismatch: exit=$exitCode stdout='$stdout' stderr='$stderr'."
        }
    }
    elseif ($exitCode -ne 0 -or $stdout -cne '' -or $stderr -cne '') {
        throw "Production PATH-helper test-hook probe mismatch: exit=$exitCode stdout='$stdout' stderr='$stderr'."
    }
    return [ordered]@{ enabled = $Enabled; exitCode = $exitCode; stdout = $stdout; stderr = $stderr }
}
$testHooksEnabled = $EnableTestHooks.IsPresent
$testBuildEnabled = $TestBuild.IsPresent
if ($testHooksEnabled -and -not $testBuildEnabled) {
    throw '-EnableTestHooks requires -TestBuild; production-shaped MSI builds cannot embed enabled failure hooks.'
}
$ManifestPath = Join-Path $ProjectRoot 'packaging\payload.json'
$VersionLedgerPath = Join-Path $ProjectRoot 'packaging\windows\versions.json'
$WixSource = Join-Path $ProjectRoot 'packaging\windows\Inlaid.wxs'

if ([string]::IsNullOrWhiteSpace($Executable)) { $Executable = Join-Path $ProjectRoot 'bin\inlaid.exe' }
elseif (-not [System.IO.Path]::IsPathRooted($Executable)) { $Executable = Join-Path $ProjectRoot $Executable }
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) { $OutputDirectory = Join-Path $ProjectRoot 'dist' }
elseif (-not [System.IO.Path]::IsPathRooted($OutputDirectory)) { $OutputDirectory = Join-Path $ProjectRoot $OutputDirectory }

$Version = $Version.Trim()
$MsiVersion = $MsiVersion.Trim()
if ($Version -notmatch '^v[0-9][0-9A-Za-z._-]*$') { throw "Version must be a leading-v executable identity: '$Version'" }
if ($MsiVersion -notmatch '^(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,4})$') { throw "MSI version must have three numeric components: '$MsiVersion'" }
if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) { throw "Release executable was not found: $Executable" }
if (-not (Test-Path -LiteralPath $WixSource -PathType Leaf)) { throw "WiX source was not found: $WixSource" }
$executablePe = Assert-InlaidAmd64Pe32Plus -Path $Executable -Description 'MSI application payload'
$identity = @(& $Executable --version)
if ($LASTEXITCODE -ne 0 -or $identity.Count -ne 1 -or $identity[0] -cne "Inlaid $Version") {
    throw "MSI executable identity must be exactly 'Inlaid $Version'; got '$($identity -join ' ')'."
}

$VersionLedger = Get-Content -LiteralPath $VersionLedgerPath -Raw | ConvertFrom-Json
if ($VersionLedger.schema -ne 1 -or $VersionLedger.upgradeCode -cne 'E1D7019B-07CD-4A8E-8D65-C6FA20B7F07D') {
    throw 'Windows MSI version ledger has an invalid schema or UpgradeCode.'
}
if (-not $TestBuild) {
    $matches = @($VersionLedger.releases | Where-Object { $_.tag -ceq $Version -and $_.msiVersion -ceq $MsiVersion })
    if ($matches.Count -ne 1) { throw "Release mapping $Version -> $MsiVersion is not recorded exactly once in packaging/windows/versions.json." }
    $duplicateMsi = @($VersionLedger.releases | Where-Object { $_.msiVersion -ceq $MsiVersion })
    if ($duplicateMsi.Count -ne 1) { throw "MSI version $MsiVersion is reused in the release ledger." }
}

$Entries = Resolve-InlaidPayload -ProjectRoot $ProjectRoot -Platform windows -Profile msi -Executable $Executable
$TemporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('inlaid-msi-' + [Guid]::NewGuid().ToString('N'))
$PayloadRoot = Join-Path $TemporaryRoot 'payload'
$PathHelper = Join-Path $TemporaryRoot 'inlaid-path-helper.exe'
try {
    foreach ($Entry in $Entries) {
        if ($null -eq $Entry.Source) { throw "MSI payload role cannot be generated at packaging time: $($Entry.Role)" }
        $Destination = [System.IO.Path]::GetFullPath((Join-Path $PayloadRoot $Entry.Destination))
        $PayloadPrefix = [System.IO.Path]::GetFullPath($PayloadRoot).TrimEnd('\') + '\'
        if (-not $Destination.StartsWith($PayloadPrefix, [System.StringComparison]::OrdinalIgnoreCase)) { throw "Payload destination escaped staging root: $($Entry.Destination)" }
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Destination) | Out-Null
        Copy-Item -LiteralPath $Entry.Source -Destination $Destination
    }
    $Expected = @($Entries | ForEach-Object { $_.Destination } | Sort-Object)
    $Actual = @(Get-ChildItem -LiteralPath $PayloadRoot -File -Recurse | ForEach-Object { $_.FullName.Substring($PayloadRoot.Length + 1) } | Sort-Object)
    if (Compare-Object -ReferenceObject $Expected -DifferenceObject $Actual) { throw 'MSI staging payload does not exactly match the resolved Windows MSI profile.' }

    Push-Location -LiteralPath $ProjectRoot
    try {
        $helperArguments = @('build', '-trimpath')
        $helperHookValue = if ($testHooksEnabled) { 'true' } else { 'false' }
        $helperArguments += @('-ldflags', "-X main.testHooks=$helperHookValue")
        $helperArguments += @('-o', $PathHelper, '.\cmd\inlaid-path-helper')
        $savedGoos, $savedGoarch, $savedCgo, $savedGoflags = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED, $env:GOFLAGS
        try {
            $env:GOOS = 'windows'
            $env:GOARCH = 'amd64'
            $env:CGO_ENABLED = '0'
            $env:GOFLAGS = ''
            & go @helperArguments
            $helperBuildExitCode = $LASTEXITCODE
        }
        finally {
            $env:GOOS, $env:GOARCH, $env:CGO_ENABLED, $env:GOFLAGS = $savedGoos, $savedGoarch, $savedCgo, $savedGoflags
        }
        if ($helperBuildExitCode -ne 0 -or -not (Test-Path -LiteralPath $PathHelper -PathType Leaf)) { throw 'Could not build the embedded PATH helper.' }
    }
    finally { Pop-Location }

    $pathHelperPe = Assert-InlaidAmd64Pe32Plus -Path $PathHelper -Description 'MSI embedded PATH helper'
    $pathHelperProbe = Assert-PathHelperTestHookMode $PathHelper $testHooksEnabled (Join-Path $TemporaryRoot 'path-helper-mode-probe.json')

    $WixCommand = if (Test-Path -LiteralPath $Wix -PathType Leaf) { (Resolve-Path -LiteralPath $Wix).Path } else {
        $wixApplication = Get-Command -Name $Wix -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $wixApplication) { $wixApplication.Source } else { '' }
    }
    if ([string]::IsNullOrWhiteSpace($WixCommand)) { throw "Pinned WiX CLI was not found: $Wix. Install it with dotnet tool install --tool-path .tools\wix wix --version 5.0.2." }
    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
    $Output = Join-Path $OutputDirectory ("inlaid-$Version-windows-x64.msi")
    & $WixCommand build $WixSource -arch x64 -d "PayloadRoot=$PayloadRoot" -d "PathHelper=$PathHelper" -d "MsiVersion=$MsiVersion" -o $Output
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $Output -PathType Leaf)) { throw 'WiX did not produce the requested MSI.' }

    $payloadEvidence = @($Entries | ForEach-Object {
        $sourceHash = (Get-FileHash -LiteralPath $_.Source -Algorithm SHA256).Hash.ToLowerInvariant()
        $stagedHash = (Get-FileHash -LiteralPath (Join-Path $PayloadRoot $_.Destination) -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($sourceHash -cne $stagedHash) { throw "Staged MSI payload differs from its resolved source: $($_.Destination)" }
        [ordered]@{ role = $_.Role; destination = $_.Destination.Replace('\', '/'); source = $_.SourceToken; sourceSHA256 = $sourceHash; stagedSHA256 = $stagedHash }
    })
    $resolved = [ordered]@{
        schema = 3; tag = $Version; msiVersion = $MsiVersion; architecture = 'amd64'
        upgradeCode = $VersionLedger.upgradeCode
        executable = $executablePe
        pathHelper = [ordered]@{ pe = $pathHelperPe; testHooks = $pathHelperProbe }
        payload = $payloadEvidence
    }
    $resolved | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath ($Output + '.payload.json') -Encoding utf8
    Write-Host "Created: $Output" -ForegroundColor Green
}
finally {
    if (Test-Path -LiteralPath $TemporaryRoot -PathType Container) {
        $temporaryBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
        $resolvedTemporary = [System.IO.Path]::GetFullPath($TemporaryRoot)
        if ($resolvedTemporary.StartsWith($temporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $resolvedTemporary -Recurse -Force
        }
    }
}
