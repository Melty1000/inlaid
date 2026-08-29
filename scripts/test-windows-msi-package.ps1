[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Wix,
    [string]$OutputDirectory = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$BuildScript = Join-Path $PSScriptRoot 'build-windows-msi.ps1'
$InspectScript = Join-Path $PSScriptRoot 'inspect-windows-msi.ps1'
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $ProjectRoot '.tools\evidence\windows-msi-package'
}
elseif (-not [System.IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = [System.IO.Path]::GetFullPath((Join-Path $ProjectRoot $OutputDirectory))
}
$RunRoot = Join-Path $OutputDirectory ('run-' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $RunRoot | Out-Null

$productionHookRejected = $false
try {
    & $BuildScript -Version 'v0.0.0-test-hook-rejection' -MsiVersion '0.0.0' `
        -Executable (Join-Path $RunRoot 'not-used.exe') -OutputDirectory (Join-Path $RunRoot 'not-created') `
        -Wix $Wix -EnableTestHooks
}
catch {
    if ($_.Exception.Message -cne '-EnableTestHooks requires -TestBuild; production-shaped MSI builds cannot embed enabled failure hooks.') { throw }
    $productionHookRejected = $true
}
if (-not $productionHookRejected) { throw 'Production-shaped MSI build unexpectedly accepted enabled failure hooks.' }

$fixtures = @(
    [pscustomobject]@{ Name = 'first'; Tag = 'v0.0.1-phase3'; MsiVersion = '0.0.1'; HookEnabled = $false; HostileGoFlags = '-ldflags=-X=main.testHooks=true' },
    [pscustomobject]@{ Name = 'second'; Tag = 'v0.0.2-phase3'; MsiVersion = '0.0.2'; HookEnabled = $true; HostileGoFlags = '-ldflags=-X=main.testHooks=false' }
)
$results = @()
foreach ($fixture in $fixtures) {
    $executable = Join-Path $RunRoot ("inlaid-$($fixture.Name).exe")
    Push-Location -LiteralPath $ProjectRoot
    try {
        $savedGoos, $savedGoarch, $savedCgo = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
        $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
        & go build -trimpath -ldflags "-X main.version=$($fixture.Tag)" -o $executable '.\cmd\inlaid'
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $executable -PathType Leaf)) {
            throw "Fixture executable build failed: $($fixture.Name)"
        }
    }
    finally {
        $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $savedGoos, $savedGoarch, $savedCgo
        Pop-Location
    }

    $packageDirectory = Join-Path $RunRoot $fixture.Name
    $savedGoFlags = $env:GOFLAGS
    try {
        $env:GOFLAGS = $fixture.HostileGoFlags
        $buildArguments = @{
            Version = $fixture.Tag; MsiVersion = $fixture.MsiVersion; Executable = $executable
            OutputDirectory = $packageDirectory; Wix = $Wix; TestBuild = $true
        }
        if ($fixture.HookEnabled) { $buildArguments.EnableTestHooks = $true }
        & $BuildScript @buildArguments
        if ($env:GOFLAGS -cne $fixture.HostileGoFlags) {
            throw "MSI builder did not restore hostile GOFLAGS for fixture $($fixture.Name)."
        }
    }
    finally { $env:GOFLAGS = $savedGoFlags }
    $msi = Join-Path $packageDirectory "inlaid-$($fixture.Tag)-windows-x64.msi"
    if (-not (Test-Path -LiteralPath $msi -PathType Leaf)) { throw "Fixture MSI is missing: $msi" }

    $evidenceDirectory = Join-Path $RunRoot ("$($fixture.Name)-evidence")
    & $InspectScript -Msi $msi -Wix $Wix -ExpectedVersion $fixture.MsiVersion -Executable $executable `
        -EvidenceDirectory $evidenceDirectory -ExpectedPathHelperTestHooks $fixture.HookEnabled
    $evidencePath = Join-Path $evidenceDirectory 'msi-evidence.json'
    $evidence = Get-Content -LiteralPath $evidencePath -Raw | ConvertFrom-Json
    $results += [pscustomobject]@{
        name = $fixture.Name
        tag = $fixture.Tag
        msiVersion = $fixture.MsiVersion
        executable = $executable
        executableSHA256 = (Get-FileHash -LiteralPath $executable -Algorithm SHA256).Hash
        msi = $msi
        msiSHA256 = [string]$evidence.msiSHA256
        evidence = $evidencePath
        evidenceSHA256 = (Get-FileHash -LiteralPath $evidencePath -Algorithm SHA256).Hash
        productCode = [string]$evidence.identity.productCode
        packageCode = [string]$evidence.identity.packageCode
        upgradeCode = [string]$evidence.identity.upgradeCode
        architecture = [string]$evidence.architecture.application.machine
        pathHelperTestHooks = [bool]$evidence.architecture.pathHelper.testHooks.enabled
        hostileGoFlags = $fixture.HostileGoFlags
        builderRestoredHostileGoFlags = $true
        payload = @($evidence.payload)
        registry = @($evidence.registry)
        unsafePostFinalizeFailureActionCount = @($evidence.customActions | Where-Object { $_.Action -ceq 'FailAfterFinalizeUserPathMarker' }).Count
    }
}

if ($results.Count -ne 2 -or $results[0].productCode -ceq $results[1].productCode -or
    $results[0].packageCode -ceq $results[1].packageCode -or
    $results[0].upgradeCode -cne $results[1].upgradeCode) {
    throw 'MSI fixture identities do not prove stable UpgradeCode with changing ProductCode and PackageCode.'
}
foreach ($result in $results) {
    if ($result.architecture -cne 'amd64' -or @($result.payload).Count -ne 6 -or
        @($result.registry).Count -ne 7 -or $result.unsafePostFinalizeFailureActionCount -ne 0 -or
        $result.pathHelperTestHooks -ne ($result.name -ceq 'second')) {
        throw "MSI fixture did not prove an amd64 application, deterministic PATH-helper hook mode, exact exported payload/registry mapping, and absence of the unsafe late-uninstall failure hook: $($result.name)"
    }
}

$set = [ordered]@{
    schema = 1
    createdUtc = [DateTime]::UtcNow.ToString('o')
    wix = $Wix
    productionHookRejected = $productionHookRejected
    runRoot = $RunRoot
    fixtures = $results
}
$setPath = Join-Path $RunRoot 'fixture-set.json'
$set | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $setPath -Encoding utf8NoBOM
Write-Host 'Pinned-WiX strict per-user build, validation, decompilation, and table tests passed.' -ForegroundColor Green
Write-Host "Fixture set: $setPath"
