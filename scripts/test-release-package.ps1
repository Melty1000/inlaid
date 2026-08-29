[CmdletBinding()]
param(
    [string]$Version = 'v0.0.0-ci',
    [string]$Executable = 'bin\inlaid.exe',
    [switch]$HashCompatibilityProbe
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'resolve-payload.ps1')
if (-not [System.IO.Path]::IsPathRooted($Executable)) { $Executable = Join-Path $ProjectRoot $Executable }
$PayloadManifestPath = Join-Path $ProjectRoot 'packaging\payload.json'
$WixSourcePath = Join-Path $ProjectRoot 'packaging\windows\Inlaid.wxs'
$MsiLifecyclePath = Join-Path $ProjectRoot 'scripts\test-windows-msi.ps1'
$MsiBuildPath = Join-Path $ProjectRoot 'scripts\build-windows-msi.ps1'
$MsiPackageTestPath = Join-Path $ProjectRoot 'scripts\test-windows-msi-package.ps1'
$TemporaryBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$TemporaryRoot = [System.IO.Path]::GetFullPath((Join-Path $TemporaryBase ('inlaid-package-test-' + [Guid]::NewGuid().ToString('N'))))
if (-not $TemporaryRoot.StartsWith($TemporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'Package test directory escaped the system temporary directory.'
}

function Get-SHA256Hex([byte[]]$Bytes) {
    $algorithm = [System.Security.Cryptography.SHA256]::Create()
    try {
        $hash = $algorithm.ComputeHash($Bytes)
        return ([System.BitConverter]::ToString($hash)).Replace('-', '').ToLowerInvariant()
    }
    finally { $algorithm.Dispose() }
}

function Get-TreeDigest([string]$Root) {
    $rows = @(Get-ChildItem -LiteralPath $Root -File -Recurse | Sort-Object FullName | ForEach-Object {
        $relative = $_.FullName.Substring($Root.Length + 1)
        "$relative=$((Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant())"
    })
    $bytes = [System.Text.Encoding]::UTF8.GetBytes(($rows -join "`n"))
    return Get-SHA256Hex $bytes
}

function Get-ExactFileEvidence([string]$Root, [string[]]$RelativePaths) {
    return @($RelativePaths | Sort-Object | ForEach-Object {
        $relative = [string]$_
        $path = Join-Path $Root $relative
        $item = Get-Item -LiteralPath $path -Force -ErrorAction Stop
        if ($item.PSIsContainer -or (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0)) {
            throw "Evidence path is not a direct regular file: $relative"
        }
        [pscustomobject]@{
            Path = $relative
            Length = [long]$item.Length
            SHA256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    })
}

function Assert-ExactFileEvidence([object[]]$Before, [object[]]$After, [string]$Description) {
    $beforeRows = @($Before | ForEach-Object { "$($_.Path)|$($_.Length)|$($_.SHA256)" })
    $afterRows = @($After | ForEach-Object { "$($_.Path)|$($_.Length)|$($_.SHA256)" })
    if (Compare-Object -ReferenceObject $beforeRows -DifferenceObject $afterRows -CaseSensitive) {
        throw "$Description did not remain byte-identical."
    }
}

function Assert-PortablePayloadMatchesManifest([string]$PortableRoot, [string]$ExpectedManifestPath) {
    $markerPath = Join-Path $PortableRoot 'inlaid-portable.json'
    if ((Get-FileHash -LiteralPath $markerPath -Algorithm SHA256).Hash.ToLowerInvariant() -cne
        (Get-FileHash -LiteralPath $ExpectedManifestPath -Algorithm SHA256).Hash.ToLowerInvariant()) {
        throw 'Portable marker did not atomically advance to the exact new versioned manifest.'
    }
    $manifest = Get-Content -LiteralPath $markerPath -Raw | ConvertFrom-Json
    foreach ($entry in @($manifest.files)) {
        $path = Join-Path $PortableRoot ([string]$entry.path)
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or
            (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne [string]$entry.sha256) {
            throw "Final portable payload does not match the new manifest: $($entry.path)"
        }
    }
}

if ($HashCompatibilityProbe) {
    $probe = Get-SHA256Hex ([System.Text.Encoding]::ASCII.GetBytes('abc'))
    if ($probe -cne 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad') {
        throw "Windows PowerShell SHA-256 compatibility probe failed: $probe"
    }
    Write-Output 'Windows PowerShell SHA-256 compatibility probe passed.'
    return
}

$WindowsPowerShell = Get-Command powershell.exe -ErrorAction SilentlyContinue

try {
    if ($null -ne $WindowsPowerShell) {
        & $WindowsPowerShell.Source -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $PSCommandPath -HashCompatibilityProbe
        if ($LASTEXITCODE -ne 0) { throw 'Release-package SHA-256 helper failed under Windows PowerShell 5.1.' }
    }
    $PayloadManifest = Get-Content -LiteralPath $PayloadManifestPath -Raw | ConvertFrom-Json
    if ($PayloadManifest.schema -ne 3) { throw "Unsupported release payload manifest schema: $($PayloadManifest.schema)" }
    $CommonPayload = @($PayloadManifest.logicalPayloads.'windows-common')
    if ($CommonPayload.Count -eq 0) { throw 'Windows channels must share one non-empty common logical payload.' }
    $MsiChannel = $PayloadManifest.channels.windows.msi
    $PortableChannel = $PayloadManifest.channels.windows.portable
    if ($MsiChannel.payload -cne 'windows-common' -or $PortableChannel.payload -cne 'windows-common' -or
        @($MsiChannel.additions).Count -ne 0 -or @($PortableChannel.additions).Count -ne 2) {
        throw 'Windows channel payload composition no longer matches the MSI and portable artifact contract.'
    }
    foreach ($future in @('linux', 'macos')) {
        $node = $PayloadManifest.channels.$future
        if ($node.status -ne 'contract-only' -or @($node.PSObject.Properties | Where-Object { $_.Name -ne 'status' }).Count -ne 0) {
            throw "$future must remain a contract-only payload seam with no implemented package channel."
        }
        if (Test-Path -LiteralPath (Join-Path $ProjectRoot "packaging\$future")) {
            throw "Phase 3 must not create packaging/$future."
        }
    }
    $MsiEntries = Resolve-InlaidPayload -ProjectRoot $ProjectRoot -Platform windows -Profile msi -Executable $Executable
    $PortableEntries = Resolve-InlaidPayload -ProjectRoot $ProjectRoot -Platform windows -Profile portable -Executable $Executable
    $PortableUpdater = @($PortableEntries | Where-Object { $_.Role -ceq 'portable-updater' })
    if ($PortableUpdater.Count -ne 1 -or $PortableUpdater[0].Destination -cne 'update-portable.ps1') {
        throw 'Portable payload must expose its update helper at the portable ZIP root.'
    }
    $Readme = Get-Content -LiteralPath (Join-Path $ProjectRoot 'README.md') -Raw
    if (-not $Readme.Contains('Expand-Archive -LiteralPath $package -DestinationPath $staging') -or
        -not $Readme.Contains('& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File') -or
        -not $Readme.Contains("Join-Path `$staging 'inlaid-vNEXT-windows-amd64\update-portable.ps1'") -or
        -not $Readme.Contains('The source checkout includes the pinned helper:')) {
        throw 'README portable update and FFmpeg instructions no longer match the new packaged helper route and source-only FFmpeg route.'
    }

    [xml]$WixSource = Get-Content -LiteralPath $WixSourcePath -Raw
    $MsiLifecycleSource = Get-Content -LiteralPath $MsiLifecyclePath -Raw
    $MsiBuildSource = Get-Content -LiteralPath $MsiBuildPath -Raw
    $MsiPackageTestSource = Get-Content -LiteralPath $MsiPackageTestPath -Raw
    foreach ($requiredHookBuildEvidence in @(
        '$savedGoos, $savedGoarch, $savedCgo, $savedGoflags =',
        ('$env:GOFLAGS = ' + "''"),
        '$helperHookValue = if ($testHooksEnabled)',
        '-X main.testHooks=$helperHookValue',
        'Assert-PathHelperTestHookMode',
        'path-helper-mode-probe.json'
    )) {
        if (-not $MsiBuildSource.Contains($requiredHookBuildEvidence)) {
            throw "MSI builder is missing deterministic PATH-helper hook evidence: $requiredHookBuildEvidence"
        }
    }
    foreach ($requiredHostileProbeEvidence in @(
        "HostileGoFlags = '-ldflags=-X=main.testHooks=true'",
        "HostileGoFlags = '-ldflags=-X=main.testHooks=false'",
        'ExpectedPathHelperTestHooks'
    )) {
        if (-not $MsiPackageTestSource.Contains($requiredHostileProbeEvidence)) {
            throw "MSI package test is missing hostile-GOFLAGS hook evidence: $requiredHostileProbeEvidence"
        }
    }
    $Namespace = [System.Xml.XmlNamespaceManager]::new($WixSource.NameTable)
    $Namespace.AddNamespace('wix', 'http://wixtoolset.org/schemas/v4/wxs')
    $Package = $WixSource.SelectSingleNode('/wix:Wix/wix:Package', $Namespace)
    if ($null -eq $Package -or $Package.Scope -ne 'perUser' -or [string]::IsNullOrWhiteSpace($Package.UpgradeCode)) {
        throw 'WiX authoring must define a per-user package with a stable upgrade identity.'
    }
    if ($WixSource.SelectNodes('//wix:StandardDirectory[@Id="LocalAppDataFolder"]/wix:Directory[@Id="PerUserProgramFilesFolder" and @Name="Programs"]/wix:Directory[@Id="INSTALLFOLDER" and @Name="Inlaid"]', $Namespace).Count -ne 1 -or
        $WixSource.SelectNodes('//wix:StandardDirectory[@Id="PerUserProgramFilesFolder" or @Id="ProgramFiles64Folder"]', $Namespace).Count -ne 0 -or
        $WixSource.SelectNodes('//wix:CustomAction[@Id="SetProgramFiles64Folder"]', $Namespace).Count -ne 0) {
        throw 'The strict per-user MSI must backport the LocalAppDataFolder/Programs/Inlaid directory tree without an unsupported standard-directory ID or global ProgramFiles64Folder override.'
    }
    if ($WixSource.SelectNodes('//wix:Property[@Id="ALLUSERS" or @Id="MSIINSTALLPERUSER"]', $Namespace).Count -ne 0) {
        throw 'A strict per-user MSI must not author the dual-purpose ALLUSERS/MSIINSTALLPERUSER properties.'
    }
    if ($WixSource.SelectNodes('//wix:Launch[@Condition="NOT RollbackDisabled"]', $Namespace).Count -ne 1) {
        throw 'The MSI must fail closed when Windows Installer rollback is disabled.'
    }
    $MsiPayload = @($WixSource.SelectNodes('//wix:File', $Namespace) | ForEach-Object { $_.Source.Replace('$(var.PayloadRoot)\', '') } | Sort-Object)
    $ExpectedMsiPayload = @($MsiEntries | ForEach-Object { $_.Destination } | Sort-Object)
    if (Compare-Object -ReferenceObject $ExpectedMsiPayload -DifferenceObject $MsiPayload) {
        throw 'WiX file authoring does not exactly match the resolved Windows MSI payload.'
    }
    if ($WixSource.SelectNodes('//wix:Shortcut|//wix:Environment', $Namespace).Count -ne 0) {
        throw 'Terminal-first MSI authoring must not contain a shortcut or Environment-table PATH edit.'
    }
    if ($WixSource.SelectNodes('//wix:Binary[@Id="InlaidPathHelper"]', $Namespace).Count -ne 1 -or
        $WixSource.SelectNodes('//wix:CustomAction[contains(@Id,"UserPath")]', $Namespace).Count -lt 4) {
        throw 'WiX authoring is missing the embedded rollback-aware user-PATH helper actions.'
    }
    $DefaultProgramDirectory = $WixSource.SelectSingleNode('//wix:CustomAction[@Id="SetINLAID_PATH_PROGRAM_DIR" and @Property="INLAID_PATH_PROGRAM_DIR"]', $Namespace)
    $DefaultProgramDirectorySequence = $WixSource.SelectSingleNode('//wix:InstallExecuteSequence/wix:Custom[@Action="SetINLAID_PATH_PROGRAM_DIR" and @After="CostFinalize"]', $Namespace)
    if ($null -eq $DefaultProgramDirectory -or $DefaultProgramDirectory.Value -cne '[INSTALLFOLDER]' -or
        $null -eq $DefaultProgramDirectorySequence -or $DefaultProgramDirectorySequence.Condition -cne 'NOT INLAID_PATH_PROGRAM_DIR') {
        throw 'WiX authoring does not default the PATH program directory to the resolved install folder.'
    }
    $DeferredCommands = [ordered]@{
        RollbackUserPath = '--action rollback'
        ApplyUserPath = '--action apply'
        UninstallUserPath = '--action uninstall'
        FinalizeUserPathMarker = '--action finalize'
        FailAfterFinalizeUserPathMarker = '--action fail'
        FailAfterUserPath = '--action fail'
        FailAfterRemoveExistingProducts = '--action fail'
        CommitUserPath = '--action commit'
    }
    foreach ($action in $DeferredCommands.Keys) {
        $custom = $WixSource.SelectSingleNode("//wix:CustomAction[@Id='$action']", $Namespace)
        if ($WixSource.SelectNodes("//wix:SetProperty[@Id='$action']", $Namespace).Count -ne 0 -or
            $null -eq $custom -or -not $custom.ExeCommand.Contains($DeferredCommands[$action]) -or
            -not $custom.ExeCommand.Contains('[TempFolder]inlaid-path-[ProductCode].json') -or
            $custom.ExeCommand.Contains('inlaid-path-E1D7019B.json') -or
            $custom.ExeCommand.Contains('[CustomActionData]')) {
            throw "Deferred MSI action $action does not embed its formatted Type-2 executable command directly."
        }
    }
    foreach ($action in @('RollbackUserPath', 'ApplyUserPath', 'UninstallUserPath')) {
        $custom = $WixSource.SelectSingleNode("//wix:CustomAction[@Id='$action']", $Namespace)
        if (-not $custom.ExeCommand.Contains('[INLAID_PATH_PROGRAM_DIR].') -or -not $custom.ExeCommand.Contains('[INSTALLFOLDER].')) {
            throw "Deferred MSI directory arguments for $action are not safe when MSI folder properties end in a backslash."
        }
    }
    foreach ($action in @('ApplyUserPath', 'UninstallUserPath')) {
        $custom = $WixSource.SelectSingleNode("//wix:CustomAction[@Id='$action']", $Namespace)
        if (-not $custom.ExeCommand.Contains('--user-sid "[UserSID]"')) {
            throw "Deferred MSI action $action does not address the explicit per-user registry hive."
        }
    }
    $PreflightAction = $WixSource.SelectSingleNode('//wix:CustomAction[@Id="PreflightUserPathState" and @Execute="immediate" and @Impersonate="yes"]', $Namespace)
    $PreflightSequence = $WixSource.SelectSingleNode('//wix:InstallExecuteSequence/wix:Custom[@Action="PreflightUserPathState" and @After="InstallInitialize"]', $Namespace)
    if ($null -eq $PreflightAction -or -not $PreflightAction.ExeCommand.Contains('[TempFolder]inlaid-path-[ProductCode].json') -or
        $null -eq $PreflightSequence -or -not $PreflightSequence.Condition.Contains('NOT UPGRADINGPRODUCTCODE')) {
        throw 'The MSI does not fail closed on stale same-ProductCode transaction state before scheduling rollback.'
    }
    $UninstallPathSequence = $WixSource.SelectSingleNode('//wix:InstallExecuteSequence/wix:Custom[@Action="UninstallUserPath" and @Before="RemoveRegistryValues"]', $Namespace)
    $FinalizeMarkerSequence = $WixSource.SelectSingleNode('//wix:InstallExecuteSequence/wix:Custom[@Action="FinalizeUserPathMarker" and @After="RemoveRegistryValues"]', $Namespace)
    if ($null -eq $UninstallPathSequence -or $null -eq $FinalizeMarkerSequence -or
        -not $FinalizeMarkerSequence.Condition.Contains('REMOVE~="ALL"') -or
        $WixSource.SelectNodes('//wix:RegistryValue[@Key="Software\Inlaid\Installer\Components"]', $Namespace).Count -eq 0 -or
        $WixSource.SelectNodes('//wix:Component[@Permanent="yes"]', $Namespace).Count -ne 0) {
        throw 'PATH provenance must be consumed before MSI removes registry values, then its exact empty marker/Components tree finalized while rollback remains available.'
    }
    $TransactionSetter = $WixSource.SelectSingleNode('//wix:SetProperty[@Id="InlaidPathTransactionActive" and @After="PreflightUserPathState" and @Condition="NOT UPGRADINGPRODUCTCODE"]', $Namespace)
    $RollbackSequence = $WixSource.SelectSingleNode('//wix:InstallExecuteSequence/wix:Custom[@Action="RollbackUserPath" and @After="SetInlaidPathTransactionActive"]', $Namespace)
    $CommitSequence = $WixSource.SelectSingleNode('//wix:InstallExecuteSequence/wix:Custom[@Action="CommitUserPath"]', $Namespace)
    $PostRemovalFailure = $WixSource.SelectSingleNode('//wix:InstallExecuteSequence/wix:Custom[@Action="FailAfterRemoveExistingProducts" and @After="RemoveExistingProducts"]', $Namespace)
    if ($null -eq $TransactionSetter -or
        $null -eq $RollbackSequence -or -not $RollbackSequence.Condition.Contains('InlaidPathTransactionActive') -or -not $RollbackSequence.Condition.Contains('NOT UPGRADINGPRODUCTCODE') -or
        $null -eq $CommitSequence -or -not $CommitSequence.Condition.Contains('InlaidPathTransactionActive') -or -not $CommitSequence.Condition.Contains('NOT UPGRADINGPRODUCTCODE') -or
        $null -eq $PostRemovalFailure -or
        -not $PostRemovalFailure.Condition.Contains('INLAID_TEST_FAIL_AFTER_REMOVE_EXISTING_PRODUCTS') -or
        -not $PostRemovalFailure.Condition.Contains('WIX_UPGRADE_DETECTED')) {
        throw 'PATH rollback/commit is not transaction-conditioned or the post-RemoveExistingProducts failure regression hook is missing.'
    }
    foreach ($action in @('RollbackUserPath', 'ApplyUserPath', 'UninstallUserPath', 'FinalizeUserPathMarker', 'FailAfterUserPath', 'FailAfterRemoveExistingProducts', 'CommitUserPath')) {
        $sequence = $WixSource.SelectSingleNode("//wix:InstallExecuteSequence/wix:Custom[@Action='$action']", $Namespace)
        if ($null -eq $sequence -or -not $sequence.Condition.Contains('NOT UPGRADINGPRODUCTCODE') -or
            -not $sequence.Condition.Contains('InlaidPathTransactionActive')) {
            throw "MSI PATH transaction action $action is not directly upgrade-excluded and privately guarded."
        }
    }
    $profileComponents = @($WixSource.SelectNodes('//wix:StandardDirectory[@Id="LocalAppDataFolder"]//wix:Component', $Namespace))
    foreach ($component in $profileComponents) {
        if ($component.SelectNodes('./wix:RegistryValue[@Root="HKCU" and @KeyPath="yes"]', $Namespace).Count -ne 1) {
            throw "ICE38 profile component $($component.Id) does not have exactly one HKCU registry key path."
        }
    }
    if ($WixSource.SelectNodes('//wix:RemoveFolder[@Directory="PerUserProgramFilesFolder"]', $Namespace).Count -ne 0) {
        throw 'The WiX 5 per-user backport must never remove the shared current-user Programs directory.'
    }
    foreach ($directory in @('INSTALLFOLDER', 'InlaidDocsFolder', 'InlaidFiltersFolder')) {
        if ($WixSource.SelectNodes("//wix:RemoveFolder[@Directory='$directory' or (not(@Directory) and ancestor::wix:Directory[1]/@Id='$directory')]", $Namespace).Count -lt 1) {
            throw "ICE64 profile directory $directory has no uninstall RemoveFolder row."
        }
    }
    foreach ($component in @($WixSource.SelectNodes('//wix:Component', $Namespace))) {
        if ([string]::IsNullOrWhiteSpace($component.Guid) -or $component.Guid -eq '*') {
            throw "MSI component $($component.Id) must have a stable explicit GUID."
        }
    }
    foreach ($requiredLifecycleEvidence in @(
        'DISABLEROLLBACK=1',
        'Assert-RollbackDisabledLog',
        'INLAID_TEST_FAIL_AFTER_REMOVE_EXISTING_PRODUCTS=1',
        'Assert-PostRemoveExistingProductsFailureLog',
        'Get-InlaidRegistrationSnapshot',
        'volatileExclusions = @()',
        'Get-UserDataSnapshot',
        'Retain-LifecycleEvidence'
    )) {
        if (-not $MsiLifecycleSource.Contains($requiredLifecycleEvidence)) {
            throw "MSI lifecycle route is missing required exact evidence: $requiredLifecycleEvidence"
        }
    }

    $leadingVRejected = $false
    try { & (Join-Path $PSScriptRoot 'package-release.ps1') -Version '0.0.0-phase3' -Executable $Executable -OutputDirectory (Join-Path $TemporaryRoot 'invalid-version') }
    catch { $leadingVRejected = $_.Exception.Message -like '*leading-v*' }
    if (-not $leadingVRejected) { throw 'Release packaging accepted an identity without the required leading v.' }
    $identityMismatchRejected = $false
    try { & (Join-Path $PSScriptRoot 'package-release.ps1') -Version 'v0.0.0-wrong' -Executable $Executable -OutputDirectory (Join-Path $TemporaryRoot 'wrong-identity') }
    catch { $identityMismatchRejected = $_.Exception.Message -like '*identity must be exactly*' }
    if (-not $identityMismatchRejected) { throw 'Release packaging accepted an executable with a different embedded identity.' }
    $wrongArchitectureExecutable = Join-Path $TemporaryRoot 'wrong-architecture-inlaid.exe'
    Push-Location -LiteralPath $ProjectRoot
    try {
        $savedGoos, $savedGoarch, $savedCgo, $savedGoflags = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED, $env:GOFLAGS
        try {
            $env:GOOS = 'windows'
            $env:GOARCH = '386'
            $env:CGO_ENABLED = '0'
            $env:GOFLAGS = ''
            & go build -trimpath -ldflags "-X main.version=$Version" -o $wrongArchitectureExecutable '.\cmd\inlaid'
            $wrongArchitectureBuildExitCode = $LASTEXITCODE
        }
        finally {
            $env:GOOS, $env:GOARCH, $env:CGO_ENABLED, $env:GOFLAGS = $savedGoos, $savedGoarch, $savedCgo, $savedGoflags
        }
    }
    finally { Pop-Location }
    if ($wrongArchitectureBuildExitCode -ne 0 -or -not (Test-Path -LiteralPath $wrongArchitectureExecutable -PathType Leaf)) {
        throw 'Could not build the version-correct wrong-architecture package fixture.'
    }
    $wrongArchitectureOutput = Join-Path $TemporaryRoot 'wrong-architecture'
    $wrongArchitectureRejected = $false
    try {
        & (Join-Path $PSScriptRoot 'package-release.ps1') -Version $Version -Executable $wrongArchitectureExecutable -OutputDirectory $wrongArchitectureOutput
    }
    catch { $wrongArchitectureRejected = $_.Exception.Message -like '*must be an amd64 PE32+ image*' }
    if (-not $wrongArchitectureRejected) { throw 'Release packaging accepted a version-correct non-amd64 executable.' }
    if (Test-Path -LiteralPath $wrongArchitectureOutput) { throw 'Release packaging created output before rejecting a non-amd64 executable.' }
    $unmappedMsiRejected = $false
    try {
        & (Join-Path $PSScriptRoot 'build-windows-msi.ps1') -Version $Version -MsiVersion '0.0.0' -Executable $Executable -OutputDirectory (Join-Path $TemporaryRoot 'unmapped-msi')
    }
    catch { $unmappedMsiRejected = $_.Exception.Message -like '*is not recorded exactly once*' }
    if (-not $unmappedMsiRejected) { throw 'MSI packaging accepted an unrecorded semantic-to-MSI version mapping.' }
    $productionHooksRejected = $false
    try {
        & (Join-Path $PSScriptRoot 'build-windows-msi.ps1') -Version $Version -MsiVersion '0.0.0' -Executable $Executable `
            -OutputDirectory (Join-Path $TemporaryRoot 'production-hooks') -EnableTestHooks
    }
    catch { $productionHooksRejected = $_.Exception.Message -like '*requires -TestBuild*' }
    if (-not $productionHooksRejected) { throw 'MSI packaging allowed enabled failure hooks without an explicit test build.' }

    $OutputDirectory = Join-Path $TemporaryRoot 'dist'
    $ExpandedDirectory = Join-Path $TemporaryRoot 'expanded'
    & (Join-Path $PSScriptRoot 'package-release.ps1') -Version $Version -Executable $Executable -OutputDirectory $OutputDirectory
    $ArchiveName = "inlaid-$Version-windows-amd64.zip"
    $ArchivePath = Join-Path $OutputDirectory $ArchiveName
    $ChecksumPath = Join-Path $OutputDirectory 'SHA256SUMS.txt'
    if (-not (Test-Path -LiteralPath $ArchivePath -PathType Leaf) -or -not (Test-Path -LiteralPath $ChecksumPath -PathType Leaf)) {
        throw 'Release package or checksum was not created.'
    }
    $ExpectedHash = ((Get-Content -LiteralPath $ChecksumPath -Raw).Trim() -split '\s+')[0]
    $ActualHash = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($ExpectedHash -cne $ActualHash) { throw "Release checksum mismatch: $ExpectedHash != $ActualHash" }

    Expand-Archive -LiteralPath $ArchivePath -DestinationPath $ExpandedDirectory
    $PackageRoot = Join-Path $ExpandedDirectory "inlaid-$Version-windows-amd64"
    $ExpectedPortable = @($PortableEntries | ForEach-Object { $_.Destination } | Sort-Object)
    $ActualPortable = @(Get-ChildItem -LiteralPath $PackageRoot -File -Recurse | ForEach-Object { $_.FullName.Substring($PackageRoot.Length + 1) } | Sort-Object)
    if (Compare-Object -ReferenceObject $ExpectedPortable -DifferenceObject $ActualPortable) {
        throw 'Portable ZIP does not exactly match the resolved Windows portable payload.'
    }
    foreach ($SourceLauncher in @('START-INLAID.cmd', 'START-INLAID.ps1')) {
        if (Test-Path -LiteralPath (Join-Path $PackageRoot $SourceLauncher)) { throw "Portable package includes source launcher: $SourceLauncher" }
    }
    foreach ($ForbiddenDirectory in @('recordings', 'snapshots', 'support-reports', '.tools')) {
        if (Test-Path -LiteralPath (Join-Path $PackageRoot $ForbiddenDirectory)) { throw "Private or generated directory entered the package: $ForbiddenDirectory" }
    }
    $PortableManifest = Get-Content -LiteralPath (Join-Path $PackageRoot 'inlaid-portable.json') -Raw | ConvertFrom-Json
    if ($PortableManifest.schema -ne 2 -or $PortableManifest.layout -ne 'portable' -or $PortableManifest.version -cne $Version) {
        throw 'Generated portable manifest identity is invalid.'
    }
    $ExpectedManifestPaths = @($PortableEntries |
        Where-Object { $_.SourceToken -cne '@generated-portable-manifest' } |
        ForEach-Object { $_.Destination } | Sort-Object)
    $ActualManifestPaths = @($PortableManifest.files |
        ForEach-Object { ([string]$_.path).Replace('/', '\') } | Sort-Object)
    if (Compare-Object -ReferenceObject $ExpectedManifestPaths -DifferenceObject $ActualManifestPaths -CaseSensitive) {
        throw 'Generated portable manifest does not name every resolved release-owned payload file exactly once.'
    }
    foreach ($entry in @($PortableManifest.files)) {
        $path = Join-Path $PackageRoot ([string]$entry.path)
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or
            (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne [string]$entry.sha256) {
            throw "Generated portable manifest did not reconcile $($entry.path)."
        }
    }
    $PackagedUpdateHelper = Join-Path $PackageRoot 'update-portable.ps1'
    if ((Get-FileHash -LiteralPath $PackagedUpdateHelper -Algorithm SHA256).Hash.ToLowerInvariant() -cne
        (Get-FileHash -LiteralPath (Join-Path $ProjectRoot 'scripts\update-portable.ps1') -Algorithm SHA256).Hash.ToLowerInvariant()) {
        throw 'Portable update helper does not exactly match the newly packaged payload.'
    }

    $PackagedExecutable = Join-Path $PackageRoot 'inlaid.exe'
    $VersionText = @(& $PackagedExecutable --version)
    if ($LASTEXITCODE -ne 0 -or $VersionText.Count -ne 1 -or $VersionText[0] -cne "Inlaid $Version") {
        throw "Packaged executable identity mismatch: $($VersionText -join ' ')"
    }
    $PreviewOne = @(& $PackagedExecutable --render-preview 80x24)
    $PreviewTwo = @(& $PackagedExecutable --render-preview 80x24)
    if ($LASTEXITCODE -ne 0 -or ($PreviewOne -join "`n") -notmatch 'INLAID' -or
        ($PreviewOne -join "`n") -cne ($PreviewTwo -join "`n")) {
        throw 'Packaged executable preview was not successful and byte-deterministic.'
    }

    $OldVersion = 'v0.0.0-portable-old'
    $OldExecutable = Join-Path $TemporaryRoot 'old-inlaid.exe'
    Push-Location -LiteralPath $ProjectRoot
    try {
        & go build -trimpath -ldflags "-X main.version=$OldVersion" -o $OldExecutable '.\cmd\inlaid'
        if ($LASTEXITCODE -ne 0) { throw 'Could not build the old portable-update fixture.' }
    }
    finally { Pop-Location }
    $OldOutput = Join-Path $TemporaryRoot 'old-dist'
    & (Join-Path $PSScriptRoot 'package-release.ps1') -Version $OldVersion -Executable $OldExecutable -OutputDirectory $OldOutput
    $OldExpanded = Join-Path $TemporaryRoot 'old-expanded'
    Expand-Archive -LiteralPath (Join-Path $OldOutput "inlaid-$OldVersion-windows-amd64.zip") -DestinationPath $OldExpanded
    $PortableRoot = Join-Path $TemporaryRoot 'portable-root'
    Copy-Item -LiteralPath (Join-Path $OldExpanded "inlaid-$OldVersion-windows-amd64") -Destination $PortableRoot -Recurse

    $obsolete = Join-Path $PortableRoot 'CHANGELOG.md'
    Set-Content -LiteralPath $obsolete -Value 'owned by old release' -Encoding utf8
    $oldMarkerPath = Join-Path $PortableRoot 'inlaid-portable.json'
    $oldMarker = Get-Content -LiteralPath $oldMarkerPath -Raw | ConvertFrom-Json
    $oldMarker.files = @($oldMarker.files) + [pscustomobject]@{
        role = 'legacy-changelog'; path = 'CHANGELOG.md'
        sha256 = (Get-FileHash -LiteralPath $obsolete -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $oldMarker | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $oldMarkerPath -Encoding utf8
    $obsoleteDigest = (Get-FileHash -LiteralPath $obsolete -Algorithm SHA256).Hash.ToLowerInvariant()
    $obsoleteProof = @($oldMarker.files | Where-Object {
        $_.role -ceq 'legacy-changelog' -and $_.path -ceq 'CHANGELOG.md' -and $_.sha256 -ceq $obsoleteDigest
    })
    if ($obsoleteProof.Count -ne 1) { throw 'Obsolete fixture is not proven release-owned by the prior manifest.' }

    $userPaths = @(
        'inlaid-settings.json', 'recordings\.recovery\active.celltape', 'recordings\keep.mp4',
        'snapshots\keep.png', 'filters\custom.cube', 'support-reports\keep.json', '.tools\ffmpeg\bin\ffmpeg.exe'
    )
    foreach ($relative in $userPaths) {
        $path = Join-Path $PortableRoot $relative
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $path) | Out-Null
        Set-Content -LiteralPath $path -Value "user-owned:$relative" -Encoding utf8
    }

    foreach ($MalformedTargetKind in @('incomplete-current', 'legacy-only')) {
        $MalformedContainer = Join-Path $TemporaryRoot ("malformed-package-$MalformedTargetKind")
        $MalformedRoot = Join-Path $MalformedContainer ("inlaid-$MalformedTargetKind-windows-amd64")
        New-Item -ItemType Directory -Force -Path $MalformedRoot | Out-Null
        if ($MalformedTargetKind -eq 'incomplete-current') {
            Copy-Item -LiteralPath (Join-Path $PackageRoot 'inlaid.exe') -Destination (Join-Path $MalformedRoot 'inlaid.exe')
            $MalformedFiles = @([ordered]@{
                role = 'application'; path = 'inlaid.exe'
                sha256 = (Get-FileHash -LiteralPath (Join-Path $MalformedRoot 'inlaid.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
            })
        }
        else {
            Set-Content -LiteralPath (Join-Path $MalformedRoot 'CHANGELOG.md') -Value 'malicious legacy-only target' -Encoding utf8
            $MalformedFiles = @([ordered]@{
                role = 'legacy-changelog'; path = 'CHANGELOG.md'
                sha256 = (Get-FileHash -LiteralPath (Join-Path $MalformedRoot 'CHANGELOG.md') -Algorithm SHA256).Hash.ToLowerInvariant()
            })
        }
        [ordered]@{
            schema = 2; layout = 'portable'; version = "v0.0.0-$MalformedTargetKind"
            files = $MalformedFiles
        } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $MalformedRoot 'inlaid-portable.json') -Encoding utf8
        $MalformedArchive = Join-Path $TemporaryRoot ("inlaid-$MalformedTargetKind-windows-amd64.zip")
        Compress-Archive -LiteralPath $MalformedRoot -DestinationPath $MalformedArchive -CompressionLevel Optimal

        $MalformedTargetRoot = Join-Path $TemporaryRoot ("portable-target-$MalformedTargetKind")
        Copy-Item -LiteralPath $PortableRoot -Destination $MalformedTargetRoot -Recurse
        $MalformedTargetDigest = Get-TreeDigest $MalformedTargetRoot
        $MalformedTransaction = Join-Path (Split-Path -Parent $MalformedTargetRoot) ('.' + (Split-Path -Leaf $MalformedTargetRoot) + '.inlaid-update')
        $MalformedRejected = $false
        try { & $PackagedUpdateHelper -Package $MalformedArchive -PortableRoot $MalformedTargetRoot }
        catch {
            $MalformedRejected = $_.Exception.Message -like '*complete current portable payload*' -or
                $_.Exception.Message -like '*outside the fixed portable payload contract*'
        }
        if (-not $MalformedRejected -or (Get-TreeDigest $MalformedTargetRoot) -cne $MalformedTargetDigest -or
            (Test-Path -LiteralPath $MalformedTransaction)) {
            throw "Malformed $MalformedTargetKind target package was not rejected before transaction or root mutation."
        }
    }

    if ($null -ne $WindowsPowerShell) {
        $PowerShell5Root = Join-Path $TemporaryRoot 'portable-powershell5'
        Copy-Item -LiteralPath $PortableRoot -Destination $PowerShell5Root -Recurse
        $PowerShell5EvidenceBefore = Get-ExactFileEvidence $PowerShell5Root $userPaths
        & $WindowsPowerShell.Source -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $PackagedUpdateHelper `
            -Package $ArchivePath -PortableRoot $PowerShell5Root
        if ($LASTEXITCODE -ne 0) { throw "Packaged portable updater failed under Windows PowerShell 5.1 with exit code $LASTEXITCODE." }
        Assert-ExactFileEvidence $PowerShell5EvidenceBefore (Get-ExactFileEvidence $PowerShell5Root $userPaths) `
            'Windows PowerShell 5.1 portable user data'
        Assert-PortablePayloadMatchesManifest $PowerShell5Root (Join-Path $PackageRoot 'inlaid-portable.json')
    }

    $ModifiedRoot = Join-Path $TemporaryRoot 'portable-modified'
    Copy-Item -LiteralPath $PortableRoot -Destination $ModifiedRoot -Recurse
    Set-Content -LiteralPath (Join-Path $ModifiedRoot 'README.md') -Value 'user modified release payload' -Encoding utf8
    $modifiedRejected = $false
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $ModifiedRoot }
    catch { $modifiedRejected = $_.Exception.Message -like '*modified*preserving*' }
    if (-not $modifiedRejected -or (Get-Content -LiteralPath (Join-Path $ModifiedRoot 'README.md') -Raw) -notlike 'user modified*') {
        throw 'Portable update did not fail closed around a modified release-owned file.'
    }

    $CollisionRoot = Join-Path $TemporaryRoot 'portable-collision'
    Copy-Item -LiteralPath $PortableRoot -Destination $CollisionRoot -Recurse
    $collisionMarkerPath = Join-Path $CollisionRoot 'inlaid-portable.json'
    $collisionMarker = Get-Content -LiteralPath $collisionMarkerPath -Raw | ConvertFrom-Json
    $collisionMarker.files = @($collisionMarker.files | Where-Object { $_.path -cne 'update-portable.ps1' })
    $collisionMarker | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $collisionMarkerPath -Encoding utf8
    Set-Content -LiteralPath (Join-Path $CollisionRoot 'update-portable.ps1') -Value 'user-owned collision' -Encoding utf8
    $collisionRejected = $false
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $CollisionRoot }
    catch { $collisionRejected = $_.Exception.Message -like '*collides with a user-owned path*' }
    if (-not $collisionRejected -or (Get-Content -LiteralPath (Join-Path $CollisionRoot 'update-portable.ps1') -Raw) -notlike 'user-owned collision*') {
        throw 'Portable update did not fail closed around a new-payload user collision.'
    }

    $UserEvidenceBefore = Get-ExactFileEvidence $PortableRoot $userPaths
    $RootCreated = (Get-Item -LiteralPath $PortableRoot).CreationTimeUtc
    $interrupted = $false
    $failureAfterObsoleteRemoval = @($PortableManifest.files).Count + 1
    try {
        & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $PortableRoot -InjectFailureAfter $failureAfterObsoleteRemoval
    }
    catch {
        $interrupted = $_.Exception.Message -like '*Injected portable-update interruption*'
        if (-not $interrupted) { throw "Portable update failed before the injected interruption: $($_.Exception.Message)" }
    }
    if (-not $interrupted) { throw 'Portable update failure injection did not interrupt the route.' }
    if (Test-Path -LiteralPath $obsolete) { throw 'Failure injection did not occur after manifest-proven obsolete-file removal.' }
    $transaction = Join-Path (Split-Path -Parent $PortableRoot) ('.' + (Split-Path -Leaf $PortableRoot) + '.inlaid-update')
    if (-not (Test-Path -LiteralPath (Join-Path $transaction 'state.json') -PathType Leaf)) {
        throw 'Interrupted portable update did not retain recoverable transaction state.'
    }
    & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $PortableRoot
    if ((Get-Item -LiteralPath $PortableRoot).CreationTimeUtc -ne $RootCreated) {
        throw 'Portable update replaced the entire portable root.'
    }
    if (Test-Path -LiteralPath $obsolete) { throw 'Manifest-proven obsolete release file survived the portable update.' }
    $UserEvidenceAfter = Get-ExactFileEvidence $PortableRoot $userPaths
    Assert-ExactFileEvidence $UserEvidenceBefore $UserEvidenceAfter 'Portable user data'
    $finalMarker = Get-Content -LiteralPath (Join-Path $PortableRoot 'inlaid-portable.json') -Raw | ConvertFrom-Json
    if ($finalMarker.version -cne $Version) { throw 'Portable update did not commit the new manifest last.' }
    Assert-PortablePayloadMatchesManifest $PortableRoot (Join-Path $PackageRoot 'inlaid-portable.json')
    if (Test-Path -LiteralPath $transaction) { throw 'Successful portable update retained transaction state.' }

    New-Item -ItemType Directory -Path $transaction | Out-Null
    $preparingPrior = Join-Path $transaction 'prior-manifest.json'
    $preparingNext = Join-Path $transaction 'next-manifest.json'
    Copy-Item -LiteralPath (Join-Path $PortableRoot 'inlaid-portable.json') -Destination $preparingPrior
    Copy-Item -LiteralPath (Join-Path $PortableRoot 'inlaid-portable.json') -Destination $preparingNext
    [ordered]@{
        schema = 2; status = 'preparing'; targetRoot = $PortableRoot
        priorManifestSHA256 = (Get-FileHash -LiteralPath $preparingPrior -Algorithm SHA256).Hash.ToLowerInvariant()
        nextManifestSHA256 = (Get-FileHash -LiteralPath $preparingNext -Algorithm SHA256).Hash.ToLowerInvariant()
    } |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $transaction 'state.json') -Encoding utf8
    Set-Content -LiteralPath (Join-Path $transaction 'uncommitted.txt') -Value 'discard me' -Encoding utf8
    & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $PortableRoot
    if (Test-Path -LiteralPath $transaction) { throw 'Interrupted preparation was not safely discarded before retry.' }

    $PoisonRoot = Join-Path $TemporaryRoot 'portable-poisoned-state'
    Copy-Item -LiteralPath $PortableRoot -Destination $PoisonRoot -Recurse
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $PoisonRoot -InjectFailureAfter 1 }
    catch { if ($_.Exception.Message -notlike '*Injected portable-update interruption*') { throw } }
    $PoisonTransaction = Join-Path (Split-Path -Parent $PoisonRoot) ('.' + (Split-Path -Leaf $PoisonRoot) + '.inlaid-update')
    $PoisonNextPath = Join-Path $PoisonTransaction 'next-manifest.json'
    $PoisonNext = Get-Content -LiteralPath $PoisonNextPath -Raw | ConvertFrom-Json
    $PoisonNext.files = @($PoisonNext.files) + [pscustomobject]@{
        role = 'poisoned-user-settings'; path = 'inlaid-settings.json'
        sha256 = (Get-FileHash -LiteralPath (Join-Path $PoisonRoot 'inlaid-settings.json') -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $PoisonNext | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $PoisonNextPath -Encoding utf8
    $PoisonStatePath = Join-Path $PoisonTransaction 'state.json'
    $PoisonState = Get-Content -LiteralPath $PoisonStatePath -Raw | ConvertFrom-Json
    $PoisonState.nextManifestSHA256 = (Get-FileHash -LiteralPath $PoisonNextPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $PoisonState | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $PoisonStatePath -Encoding utf8
    $PoisonDigest = Get-TreeDigest $PoisonRoot
    $poisonRejected = $false
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $PoisonRoot }
    catch { $poisonRejected = $_.Exception.Message -like '*claims a user-writable path*' }
    if (-not $poisonRejected -or (Get-TreeDigest $PoisonRoot) -cne $PoisonDigest) {
        throw 'Portable-update recovery did not fail closed around a consistently hashed sensitive-path poison state.'
    }

    $CoordinatedPoisonRoot = Join-Path $TemporaryRoot 'portable-coordinated-poison'
    Copy-Item -LiteralPath $PortableRoot -Destination $CoordinatedPoisonRoot -Recurse
    $ordinaryUserPath = Join-Path $CoordinatedPoisonRoot 'ordinary-user-note.txt'
    Set-Content -LiteralPath $ordinaryUserPath -Value 'ordinary user content' -Encoding utf8
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $CoordinatedPoisonRoot -InjectFailureAfter 1 }
    catch { if ($_.Exception.Message -notlike '*Injected portable-update interruption*') { throw } }
    $CoordinatedTransaction = Join-Path (Split-Path -Parent $CoordinatedPoisonRoot) ('.' + (Split-Path -Leaf $CoordinatedPoisonRoot) + '.inlaid-update')
    $CoordinatedPriorPath = Join-Path $CoordinatedTransaction 'prior-manifest.json'
    $CoordinatedNextPath = Join-Path $CoordinatedTransaction 'next-manifest.json'
    $CoordinatedPrior = Get-Content -LiteralPath $CoordinatedPriorPath -Raw | ConvertFrom-Json
    $CoordinatedNext = Get-Content -LiteralPath $CoordinatedNextPath -Raw | ConvertFrom-Json
    $maliciousBackup = Join-Path $CoordinatedTransaction 'backup\ordinary-user-note.txt'
    Set-Content -LiteralPath $maliciousBackup -Value 'attacker replacement' -Encoding utf8
    $CoordinatedPrior.files = @($CoordinatedPrior.files) + [pscustomobject]@{
        role = 'poisoned-ordinary-note'; path = 'ordinary-user-note.txt'
        sha256 = (Get-FileHash -LiteralPath $maliciousBackup -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $CoordinatedNext.files = @($CoordinatedNext.files) + [pscustomobject]@{
        role = 'poisoned-ordinary-note'; path = 'ordinary-user-note.txt'
        sha256 = (Get-FileHash -LiteralPath $ordinaryUserPath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $CoordinatedPrior | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $CoordinatedPriorPath -Encoding utf8
    $CoordinatedNext | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $CoordinatedNextPath -Encoding utf8
    Copy-Item -LiteralPath $CoordinatedPriorPath -Destination (Join-Path $CoordinatedPoisonRoot 'inlaid-portable.json') -Force
    $CoordinatedStatePath = Join-Path $CoordinatedTransaction 'state.json'
    $CoordinatedState = Get-Content -LiteralPath $CoordinatedStatePath -Raw | ConvertFrom-Json
    $CoordinatedState.priorManifestSHA256 = (Get-FileHash -LiteralPath $CoordinatedPriorPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $CoordinatedState.nextManifestSHA256 = (Get-FileHash -LiteralPath $CoordinatedNextPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $CoordinatedState.oldExisting = @($CoordinatedState.oldExisting) + 'ordinary-user-note.txt'
    $CoordinatedState | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $CoordinatedStatePath -Encoding utf8
    $CoordinatedPoisonDigest = Get-TreeDigest $CoordinatedPoisonRoot
    $coordinatedPoisonRejected = $false
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $CoordinatedPoisonRoot }
    catch { $coordinatedPoisonRejected = $_.Exception.Message -like '*outside the fixed portable payload contract*' }
    if (-not $coordinatedPoisonRejected -or (Get-TreeDigest $CoordinatedPoisonRoot) -cne $CoordinatedPoisonDigest) {
        throw 'Portable-update recovery did not reject a self-consistent ordinary-file manifest/state/backup poison before root mutation.'
    }

    $InconsistentRoot = Join-Path $TemporaryRoot 'portable-inconsistent-state'
    Copy-Item -LiteralPath $PortableRoot -Destination $InconsistentRoot -Recurse
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $InconsistentRoot -InjectFailureAfter 1 }
    catch { if ($_.Exception.Message -notlike '*Injected portable-update interruption*') { throw } }
    $InconsistentTransaction = Join-Path (Split-Path -Parent $InconsistentRoot) ('.' + (Split-Path -Leaf $InconsistentRoot) + '.inlaid-update')
    $InconsistentStatePath = Join-Path $InconsistentTransaction 'state.json'
    $InconsistentState = Get-Content -LiteralPath $InconsistentStatePath -Raw | ConvertFrom-Json
    $InconsistentState.oldExisting = @($InconsistentState.oldExisting) + 'inlaid-settings.json'
    $InconsistentState | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $InconsistentStatePath -Encoding utf8
    $InconsistentDigest = Get-TreeDigest $InconsistentRoot
    $inconsistentRejected = $false
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $InconsistentRoot }
    catch { $inconsistentRejected = $_.Exception.Message -like '*unowned backup path*' }
    if (-not $inconsistentRejected -or (Get-TreeDigest $InconsistentRoot) -cne $InconsistentDigest) {
        throw 'Portable-update recovery mutated the root before rejecting inconsistent state.'
    }

    $CorruptBackupRoot = Join-Path $TemporaryRoot 'portable-corrupt-backup'
    Copy-Item -LiteralPath $PortableRoot -Destination $CorruptBackupRoot -Recurse
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $CorruptBackupRoot -InjectFailureAfter 1 }
    catch { if ($_.Exception.Message -notlike '*Injected portable-update interruption*') { throw } }
    $CorruptBackupTransaction = Join-Path (Split-Path -Parent $CorruptBackupRoot) ('.' + (Split-Path -Leaf $CorruptBackupRoot) + '.inlaid-update')
    Set-Content -LiteralPath (Join-Path $CorruptBackupTransaction 'backup\README.md') -Value 'corrupt backup' -Encoding utf8
    $CorruptBackupDigest = Get-TreeDigest $CorruptBackupRoot
    $backupRejected = $false
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $CorruptBackupRoot }
    catch { $backupRejected = $_.Exception.Message -like '*backup hash mismatch*' }
    if (-not $backupRejected -or (Get-TreeDigest $CorruptBackupRoot) -cne $CorruptBackupDigest) {
        throw 'Portable-update recovery mutated the root before rejecting a corrupt backup.'
    }

    $CorruptMarkerRoot = Join-Path $TemporaryRoot 'portable-corrupt-marker'
    Copy-Item -LiteralPath $PortableRoot -Destination $CorruptMarkerRoot -Recurse
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $CorruptMarkerRoot -InjectFailureAfter 1 }
    catch { if ($_.Exception.Message -notlike '*Injected portable-update interruption*') { throw } }
    Set-Content -LiteralPath (Join-Path $CorruptMarkerRoot 'inlaid-portable.json') -Value 'corrupt marker' -Encoding utf8
    $CorruptMarkerDigest = Get-TreeDigest $CorruptMarkerRoot
    $markerRejected = $false
    try { & $PackagedUpdateHelper -Package $ArchivePath -PortableRoot $CorruptMarkerRoot }
    catch { $markerRejected = $_.Exception.Message -like '*current marker does not match*' }
    if (-not $markerRejected -or (Get-TreeDigest $CorruptMarkerRoot) -cne $CorruptMarkerDigest) {
        throw 'Portable-update recovery mutated the root before rejecting a corrupt current marker.'
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

Write-Host 'Release package, portable update, and noninteractive command tests passed.' -ForegroundColor Green
