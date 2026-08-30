[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Msi,
    [Parameter(Mandatory)][string]$Wix,
    [Parameter(Mandatory)][string]$ExpectedVersion,
    [Parameter(Mandatory)][string]$Executable,
    [Parameter(Mandatory)][string]$EvidenceDirectory,
    [Parameter(Mandatory)][bool]$ExpectedPathHelperTestHooks
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$ExpectedUpgradeCode = '{E1D7019B-07CD-4A8E-8D65-C6FA20B7F07D}'
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'resolve-payload.ps1')

function Get-Amd64PeEvidence([string]$Path, [string]$Description) {
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 0x100 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) { throw "$Description is not a PE image: $Path" }
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
    if ($peOffset -lt 0x40 -or $peOffset + 26 -gt $bytes.Length -or $bytes[$peOffset] -ne 0x50 -or $bytes[$peOffset + 1] -ne 0x45) { throw "$Description has an invalid PE header: $Path" }
    $machine = [BitConverter]::ToUInt16($bytes, $peOffset + 4)
    $optionalMagic = [BitConverter]::ToUInt16($bytes, $peOffset + 24)
    if ($machine -ne 0x8664 -or $optionalMagic -ne 0x20b) { throw "$Description is not amd64 PE32+: $Path" }
    return [ordered]@{ machine = 'amd64'; machineCode = '0x8664'; format = 'PE32+'; length = $bytes.Length; sha256 = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }
}

function Resolve-Application([string]$Value) {
    if (Test-Path -LiteralPath $Value -PathType Leaf) { return (Resolve-Path -LiteralPath $Value).Path }
    $application = Get-Command -Name $Value -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $application) { throw "Application was not found: $Value" }
    return $application.Source
}

function Release-ComObject([object]$Value) {
    if ($null -ne $Value -and [Runtime.InteropServices.Marshal]::IsComObject($Value)) {
        [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($Value)
    }
}

function Read-MsiRows([object]$Database, [string]$Sql, [string[]]$Columns) {
    $rows = @()
    $view = $null
    try {
        $view = $Database.OpenView($Sql)
        [void]$view.Execute()
        while ($true) {
            $record = $view.Fetch()
            if ($null -eq $record) { break }
            try {
                $values = [ordered]@{}
                for ($index = 0; $index -lt $Columns.Count; $index++) {
                    $values[$Columns[$index]] = [string]$record.StringData($index + 1)
                }
                $rows += [pscustomobject]$values
            }
            finally { Release-ComObject $record }
            $record = $null
        }
    }
    finally {
        if ($null -ne $view) {
            try { [void]$view.Close() }
            finally { Release-ComObject $view }
        }
    }
    return @($rows)
}

function Require([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Assert-PathHelperTestHookMode([string]$Path, [bool]$Enabled, [string]$ProbeStatePath) {
    foreach ($candidate in @($ProbeStatePath, "$ProbeStatePath.partial", "$ProbeStatePath.claim", "$ProbeStatePath.claim.partial")) {
        Require (-not (Test-Path -LiteralPath $candidate)) "PATH-helper mode probe state already exists: $candidate"
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
        Require ($process.Start()) 'Exported PATH-helper mode probe process did not start.'
        $stdout = $process.StandardOutput.ReadToEnd()
        $stderr = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        $exitCode = $process.ExitCode
    }
    finally { $process.Dispose() }
    foreach ($candidate in @($ProbeStatePath, "$ProbeStatePath.partial", "$ProbeStatePath.claim", "$ProbeStatePath.claim.partial")) {
        Require (-not (Test-Path -LiteralPath $candidate)) "Exported PATH-helper probe unexpectedly created transaction state: $candidate"
    }
    $failureText = 'Inlaid PATH helper: injected failure after PATH mutation' + "`n"
    if ($Enabled) {
        Require ($exitCode -eq 1 -and $stdout -ceq '' -and $stderr -ceq $failureText) "Exported enabled PATH-helper mode mismatch: exit=$exitCode stdout='$stdout' stderr='$stderr'."
    }
    else {
        Require ($exitCode -eq 0 -and $stdout -ceq '' -and $stderr -ceq '') "Exported production PATH-helper mode mismatch: exit=$exitCode stdout='$stdout' stderr='$stderr'."
    }
    return [ordered]@{ enabled = $Enabled; exitCode = $exitCode; stdout = $stdout; stderr = $stderr }
}

$Msi = (Resolve-Path -LiteralPath $Msi).Path
$Executable = (Resolve-Path -LiteralPath $Executable).Path
$Wix = Resolve-Application $Wix
if (-not [System.IO.Path]::IsPathRooted($EvidenceDirectory)) {
    $EvidenceDirectory = [System.IO.Path]::GetFullPath((Join-Path (Get-Location) $EvidenceDirectory))
}
if (Test-Path -LiteralPath $EvidenceDirectory) {
    if (@(Get-ChildItem -LiteralPath $EvidenceDirectory -Force).Count -ne 0) {
        throw "MSI evidence directory must be empty: $EvidenceDirectory"
    }
}
else { New-Item -ItemType Directory -Path $EvidenceDirectory | Out-Null }

$wixVersion = @(& $Wix --version)
Require ($LASTEXITCODE -eq 0 -and $wixVersion.Count -eq 1 -and $wixVersion[0] -match '^5\.0\.2(?:\+|$)') 'MSI evidence requires the pinned WiX 5.0.2 CLI.'

$intermediate = Join-Path $EvidenceDirectory 'intermediate'
New-Item -ItemType Directory -Path $intermediate | Out-Null
$pdb = [System.IO.Path]::ChangeExtension($Msi, '.wixpdb')
$validateArguments = @('msi', 'validate', '-intermediateFolder', $intermediate, '-sice', 'ICE64')
if (Test-Path -LiteralPath $pdb -PathType Leaf) { $validateArguments += @('-pdb', $pdb) }
$validateArguments += $Msi
$validationOutput = @(& $Wix @validateArguments 2>&1)
if ($LASTEXITCODE -ne 0) { throw "WiX MSI validation failed: $($validationOutput -join [Environment]::NewLine)" }

$ice64Intermediate = Join-Path $EvidenceDirectory 'ice64-intermediate'
New-Item -ItemType Directory -Path $ice64Intermediate | Out-Null
$ice64Arguments = @('msi', 'validate', '-intermediateFolder', $ice64Intermediate, '-ice', 'ICE64')
if (Test-Path -LiteralPath $pdb -PathType Leaf) { $ice64Arguments += @('-pdb', $pdb) }
$ice64Arguments += $Msi
$ice64Output = @(& $Wix @ice64Arguments 2>&1)
$ice64ExitCode = $LASTEXITCODE
$ice64Text = $ice64Output -join [Environment]::NewLine
$ice64Reports = [regex]::Matches($ice64Text, 'ICE64:')
Require ($ice64ExitCode -ne 0 -and $ice64Reports.Count -eq 1 -and
    $ice64Text.Contains('directory PerUserProgramFilesFolder is in the user profile but is not listed in the RemoveFile table') -and
    -not $ice64Text.Contains('directory INSTALLFOLDER is in the user profile') -and
    -not $ice64Text.Contains('directory InlaidDocsFolder is in the user profile') -and
    -not $ice64Text.Contains('directory InlaidFiltersFolder is in the user profile')) 'ICE64 evidence must contain only the expected shared Programs-directory report.'

$decompiled = Join-Path $EvidenceDirectory 'decompiled.wxs'
$exported = Join-Path $EvidenceDirectory 'exported'
$decompileOutput = @(& $Wix msi decompile -intermediateFolder $intermediate -o $decompiled -x $exported $Msi 2>&1)
if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $decompiled -PathType Leaf)) {
    throw "WiX MSI decompilation failed: $($decompileOutput -join [Environment]::NewLine)"
}

[xml]$source = Get-Content -LiteralPath $decompiled -Raw
$namespace = [System.Xml.XmlNamespaceManager]::new($source.NameTable)
$namespace.AddNamespace('wix', 'http://wixtoolset.org/schemas/v4/wxs')
$package = $source.SelectSingleNode('/wix:Wix/wix:Package', $namespace)
Require ($null -ne $package -and $package.Scope -ceq 'perUser') 'Decompiled MSI is not strictly per-user.'
Require ($package.UpgradeCode -ceq $ExpectedUpgradeCode -and $package.Version -ceq $ExpectedVersion) 'Decompiled MSI identity mismatch.'
Require ($source.SelectNodes('//wix:StandardDirectory[@Id="LocalAppDataFolder"]/wix:Directory[@Id="PerUserProgramFilesFolder" and @Name="Programs"]/wix:Directory[@Id="INSTALLFOLDER" and @Name="Inlaid"]', $namespace).Count -eq 1) 'Decompiled MSI does not contain the WiX 5 per-user Programs directory backport.'
Require ($source.SelectNodes('//wix:StandardDirectory[@Id="PerUserProgramFilesFolder" or @Id="ProgramFiles64Folder"]', $namespace).Count -eq 0) 'Decompiled MSI contains an unsupported per-user standard directory or global ProgramFiles64Folder root.'
Require ($source.SelectNodes('//wix:CustomAction[@Id="SetProgramFiles64Folder"]', $namespace).Count -eq 0) 'Decompiled MSI globally overrides ProgramFiles64Folder.'
Require ($source.SelectNodes('//wix:Property[@Id="ALLUSERS" or @Id="MSIINSTALLPERUSER"]', $namespace).Count -eq 0) 'Decompiled strict per-user MSI contains dual-purpose properties.'
Require ($source.SelectNodes('//wix:Launch[@Condition="NOT RollbackDisabled"]', $namespace).Count -eq 1) 'Decompiled MSI does not fail closed when rollback is disabled.'
Require ($source.SelectNodes('//wix:Shortcut|//wix:Environment', $namespace).Count -eq 0) 'Decompiled terminal-first MSI contains a Shortcut or Environment element.'
Require ($source.SelectNodes('//wix:Component[not(@Bitness="always64")]', $namespace).Count -eq 0) 'Decompiled MSI contains a component that is not 64-bit.'
Require ($source.SelectNodes('//wix:CustomAction[@Impersonate="no"]', $namespace).Count -eq 0) 'Decompiled MSI contains a non-impersonated custom action.'

$installer = New-Object -ComObject WindowsInstaller.Installer
$database = $installer.OpenDatabase($Msi, 0)
$properties = Read-MsiRows $database 'SELECT `Property`, `Value` FROM `Property`' @('Property', 'Value')
$propertyMap = @{}
foreach ($row in $properties) { $propertyMap[$row.Property] = $row.Value }
Require ($propertyMap['ProductName'] -ceq 'Inlaid' -and $propertyMap['ProductVersion'] -ceq $ExpectedVersion) 'MSI Property table product identity mismatch.'
Require ($propertyMap['UpgradeCode'] -ceq $ExpectedUpgradeCode) 'MSI Property table UpgradeCode mismatch.'
Require (-not $propertyMap.ContainsKey('ALLUSERS') -and -not $propertyMap.ContainsKey('MSIINSTALLPERUSER')) 'MSI Property table contains dual-purpose install controls.'
$productCode = [string]$propertyMap['ProductCode']
$parsedGuid = [guid]::Empty
Require ([guid]::TryParse($productCode, [ref]$parsedGuid)) 'MSI ProductCode is not a GUID.'

$directories = Read-MsiRows $database 'SELECT `Directory`, `Directory_Parent`, `DefaultDir` FROM `Directory`' @('Directory', 'Parent', 'Default')
$localAppData = @($directories | Where-Object { $_.Directory -ceq 'LocalAppDataFolder' -and $_.Parent -ceq 'TARGETDIR' })
$perUserPrograms = @($directories | Where-Object { $_.Directory -ceq 'PerUserProgramFilesFolder' -and $_.Parent -ceq 'LocalAppDataFolder' -and $_.Default -ceq 'Programs' })
$installFolder = @($directories | Where-Object { $_.Directory -ceq 'INSTALLFOLDER' -and $_.Parent -ceq 'PerUserProgramFilesFolder' -and $_.Default -ceq 'Inlaid' })
Require ($localAppData.Count -eq 1 -and $perUserPrograms.Count -eq 1 -and $installFolder.Count -eq 1) 'MSI Directory table does not map LocalAppDataFolder to Programs\Inlaid.'
Require (@($directories | Where-Object { $_.Directory -ceq 'ProgramFiles64Folder' }).Count -eq 0) 'MSI Directory table contains the superseded ProgramFiles64Folder root.'

$expectedComponents = [ordered]@{
    InlaidExecutable = @('{882B8192-1A62-4B36-884A-4227DBA1DD7B}', 'INSTALLFOLDER', 260, '')
    InlaidReadme = @('{F2503590-7052-453B-A7DA-D31BD1426CC7}', 'INSTALLFOLDER', 260, '')
    InlaidLicense = @('{B1132EB7-D31B-40E1-BBC3-04F0E1AB4BF4}', 'INSTALLFOLDER', 260, '')
    InlaidNotices = @('{873F13DF-4A0F-4EA3-A09C-8D22BFE35667}', 'INSTALLFOLDER', 260, '')
    InlaidFilterDocumentation = @('{55C8F27E-9D2F-4DDD-AE65-1EFAB7B43B42}', 'InlaidDocsFolder', 260, '')
    InlaidFilterReadme = @('{12CF70D6-D308-4D00-A2A1-1F818A67ED75}', 'InlaidFiltersFolder', 260, '')
    InlaidPathProvenance = @('{BA72C6B8-2ECE-4908-8D38-D17959D78636}', 'INSTALLFOLDER', 260, '')
}
$components = Read-MsiRows $database 'SELECT `Component`, `ComponentId`, `Directory_`, `Attributes`, `Condition`, `KeyPath` FROM `Component`' @('Component', 'Guid', 'Directory', 'Attributes', 'Condition', 'KeyPath')
Require ($components.Count -eq $expectedComponents.Count) 'MSI Component table count mismatch.'
foreach ($name in $expectedComponents.Keys) {
    $component = @($components | Where-Object { $_.Component -ceq $name })
    $expected = $expectedComponents[$name]
    Require ($component.Count -eq 1 -and $component[0].Guid -ceq $expected[0] -and
        $component[0].Directory -ceq $expected[1] -and [int]$component[0].Attributes -eq [int]$expected[2]) "MSI component mismatch: $name"
    if (-not [string]::IsNullOrWhiteSpace([string]$expected[3])) {
        Require ($component[0].KeyPath -ceq $expected[3]) "MSI component key path mismatch: $name"
    }
}

$files = Read-MsiRows $database 'SELECT `File`, `Component_` FROM `File`' @('File', 'Component')
$expectedFiles = [ordered]@{
    inlaidExe = @('InlaidExecutable', 'INSTALLFOLDER')
    inlaidReadme = @('InlaidReadme', 'INSTALLFOLDER')
    inlaidLicense = @('InlaidLicense', 'INSTALLFOLDER')
    inlaidNotices = @('InlaidNotices', 'INSTALLFOLDER')
    inlaidFiltersDoc = @('InlaidFilterDocumentation', 'InlaidDocsFolder')
    inlaidFiltersReadme = @('InlaidFilterReadme', 'InlaidFiltersFolder')
}
Require ($files.Count -eq $expectedFiles.Count) 'MSI File table row count mismatch.'
$fileEvidence = @()
foreach ($fileId in $expectedFiles.Keys) {
    $expected = $expectedFiles[$fileId]
    $file = @($files | Where-Object { $_.File -ceq $fileId -and $_.Component -ceq $expected[0] })
    $component = @($components | Where-Object { $_.Component -ceq $expected[0] -and $_.Directory -ceq $expected[1] })
    Require ($file.Count -eq 1 -and $component.Count -eq 1) "MSI File/Component/Directory mapping mismatch: $fileId"
    $fileEvidence += [ordered]@{ file = $fileId; component = $expected[0]; directory = $expected[1] }
}

$buildEvidencePath = $Msi + '.payload.json'
Require (Test-Path -LiteralPath $buildEvidencePath -PathType Leaf) 'MSI build payload evidence sidecar is missing.'
$buildEvidence = Get-Content -LiteralPath $buildEvidencePath -Raw | ConvertFrom-Json
Require ($buildEvidence.schema -eq 3 -and $buildEvidence.architecture -ceq 'amd64' -and $buildEvidence.msiVersion -ceq $ExpectedVersion) 'MSI build payload evidence identity mismatch.'
Require ([bool]$buildEvidence.pathHelper.testHooks.enabled -eq $ExpectedPathHelperTestHooks) 'MSI build evidence PATH-helper test-hook mode mismatch.'
$resolvedEntries = Resolve-InlaidPayload -ProjectRoot $ProjectRoot -Platform windows -Profile msi -Executable $Executable
$fileIds = @{
    'inlaid.exe' = 'inlaidExe'; 'README.md' = 'inlaidReadme'; 'LICENSE' = 'inlaidLicense'
    'THIRD_PARTY_NOTICES.md' = 'inlaidNotices'; 'docs\FILTERS.md' = 'inlaidFiltersDoc'; 'filters\README.md' = 'inlaidFiltersReadme'
}
$payloadEvidence = @($resolvedEntries | ForEach-Object {
    $destination = $_.Destination.Replace('/', '\')
    $fileId = [string]$fileIds[$destination]
    Require (-not [string]::IsNullOrWhiteSpace($fileId)) "No MSI File-table mapping exists for resolved payload $destination."
    $exportedPath = Join-Path $exported ("File\$fileId")
    Require (Test-Path -LiteralPath $exportedPath -PathType Leaf) "Decompiled MSI payload is missing: $fileId"
    $sourceHash = (Get-FileHash -LiteralPath $_.Source -Algorithm SHA256).Hash.ToLowerInvariant()
    $exportedHash = (Get-FileHash -LiteralPath $exportedPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $built = @($buildEvidence.payload | Where-Object { ([string]$_.destination).Replace('/', '\') -ceq $destination })
    Require ($built.Count -eq 1 -and [string]$built[0].sourceSHA256 -ceq $sourceHash -and
        [string]$built[0].stagedSHA256 -ceq $sourceHash -and $exportedHash -ceq $sourceHash) "Exported MSI payload bytes do not match the resolved source/build evidence: $destination"
    [ordered]@{ role = $_.Role; destination = $_.Destination.Replace('\', '/'); fileId = $fileId; length = (Get-Item -LiteralPath $exportedPath).Length; sourceSHA256 = $sourceHash; exportedSHA256 = $exportedHash }
})
Require ($payloadEvidence.Count -eq $files.Count) 'Resolved/exported MSI payload count mismatch.'
$applicationPe = Get-Amd64PeEvidence (Join-Path $exported 'File\inlaidExe') 'Exported MSI application payload'
$helperExport = Join-Path $exported 'Binary\InlaidPathHelper'
Require (Test-Path -LiteralPath $helperExport -PathType Leaf) 'Decompiled MSI PATH helper binary is missing.'
$helperPe = Get-Amd64PeEvidence $helperExport 'Exported MSI PATH helper'
$helperProbe = Assert-PathHelperTestHookMode $helperExport $ExpectedPathHelperTestHooks (Join-Path $EvidenceDirectory 'exported-path-helper-mode-probe.json')
Require ([string]$buildEvidence.executable.sha256 -ceq $applicationPe.sha256 -and [string]$buildEvidence.pathHelper.pe.sha256 -ceq $helperPe.sha256) 'Exported MSI PE hashes do not match build evidence.'

$tables = @(Read-MsiRows $database 'SELECT `Name` FROM `_Tables`' @('Name') | ForEach-Object Name)
Require ($tables -notcontains 'Shortcut' -and $tables -notcontains 'Environment') 'MSI contains a Shortcut or Environment table.'

$registry = Read-MsiRows $database 'SELECT `Registry`, `Root`, `Key`, `Name`, `Value`, `Component_` FROM `Registry`' @('Registry', 'Root', 'Key', 'Name', 'Value', 'Component')
$expectedRegistry = [ordered]@{
    InlaidExecutable = @('Software\Inlaid\Installer\Components', 'InlaidExecutable')
    InlaidReadme = @('Software\Inlaid\Installer\Components', 'InlaidReadme')
    InlaidLicense = @('Software\Inlaid\Installer\Components', 'InlaidLicense')
    InlaidNotices = @('Software\Inlaid\Installer\Components', 'InlaidNotices')
    InlaidFilterDocumentation = @('Software\Inlaid\Installer\Components', 'InlaidFilterDocumentation')
    InlaidFilterReadme = @('Software\Inlaid\Installer\Components', 'InlaidFilterReadme')
    InlaidPathProvenance = @('Software\Inlaid\Installer', 'Component')
}
Require ($registry.Count -eq $expectedRegistry.Count) 'MSI Registry table row count mismatch.'
foreach ($componentName in $expectedRegistry.Keys) {
    $expected = $expectedRegistry[$componentName]
    $row = @($registry | Where-Object {
        $_.Component -ceq $componentName -and $_.Root -ceq '1' -and $_.Key -ceq $expected[0] -and
        $_.Name -ceq $expected[1] -and $_.Value -ceq '#1'
    })
    $component = @($components | Where-Object { $_.Component -ceq $componentName })
    Require ($row.Count -eq 1 -and $component.Count -eq 1 -and
        $component[0].KeyPath -ceq $row[0].Registry) "MSI Registry/Component key-path mapping mismatch: $componentName"
}
$removeFolders = Read-MsiRows $database 'SELECT `FileKey`, `Component_`, `FileName`, `DirProperty`, `InstallMode` FROM `RemoveFile`' @('FileKey', 'Component', 'FileName', 'Directory', 'InstallMode')
Require (@($removeFolders | Where-Object { $_.Directory -ceq 'PerUserProgramFilesFolder' }).Count -eq 0) 'MSI must preserve the shared current-user Programs directory unconditionally.'
foreach ($directory in @('INSTALLFOLDER', 'InlaidDocsFolder', 'InlaidFiltersFolder')) {
    Require (@($removeFolders | Where-Object { $_.Directory -ceq $directory -and [string]::IsNullOrEmpty($_.FileName) -and $_.InstallMode -ceq '2' }).Count -ge 1) "ICE64 profile directory has no uninstall RemoveFile row: $directory"
}

$customActions = Read-MsiRows $database 'SELECT `Action`, `Type`, `Source`, `Target` FROM `CustomAction`' @('Action', 'Type', 'Source', 'Target')
$pathDirectoryAction = @($customActions | Where-Object { $_.Action -ceq 'SetINLAID_PATH_PROGRAM_DIR' })
Require (@($customActions | Where-Object { $_.Action -ceq 'SetProgramFiles64Folder' }).Count -eq 0) 'MSI retains the superseded ProgramFiles64Folder override.'
Require ($pathDirectoryAction.Count -eq 1 -and [int]$pathDirectoryAction[0].Type -eq 51 -and
    $pathDirectoryAction[0].Source -ceq 'INLAID_PATH_PROGRAM_DIR' -and $pathDirectoryAction[0].Target -ceq '[INSTALLFOLDER]') 'MSI does not capture the resolved install directory for PATH ownership.'
foreach ($name in @('RollbackUserPath', 'ApplyUserPath', 'UninstallUserPath')) {
    $pathAction = @($customActions | Where-Object { $_.Action -ceq $name })
    Require ($pathAction.Count -eq 1 -and $pathAction[0].Target.Contains('[INLAID_PATH_PROGRAM_DIR].') -and
        $pathAction[0].Target.Contains('[INSTALLFOLDER].')) "MSI formatted directory arguments are not safe for trailing-backslash folder properties: $name"
}
foreach ($name in @('ApplyUserPath', 'UninstallUserPath')) {
    $pathAction = @($customActions | Where-Object { $_.Action -ceq $name })
    Require ($pathAction.Count -eq 1 -and $pathAction[0].Target.Contains('--user-sid "[UserSID]"')) "MSI deferred action does not address the explicit per-user registry hive: $name"
}
foreach ($name in @('SetRollbackUserPath', 'SetApplyUserPath', 'SetUninstallUserPath', 'SetFinalizeUserPathMarker', 'SetFailAfterFinalizeUserPathMarker', 'SetFailAfterUserPath', 'SetFailAfterRemoveExistingProducts', 'SetCommitUserPath')) {
    Require (@($customActions | Where-Object { $_.Action -ceq $name }).Count -eq 0) "MSI retains obsolete deferred-data setter: $name"
}
$preflightAction = @($customActions | Where-Object { $_.Action -ceq 'PreflightUserPathState' })
Require ($preflightAction.Count -eq 1 -and [int]$preflightAction[0].Type -eq 2 -and
    $preflightAction[0].Source -ceq 'InlaidPathHelper' -and
    $preflightAction[0].Target.Contains('--action preflight') -and
    $preflightAction[0].Target.Contains('[TempFolder]inlaid-path-[ProductCode].json')) 'MSI lacks the immediate stale-transaction preflight.'
$transactionSetter = @($customActions | Where-Object { $_.Action -ceq 'SetInlaidPathTransactionActive' })
Require ($transactionSetter.Count -eq 1 -and [int]$transactionSetter[0].Type -eq 51 -and
    $transactionSetter[0].Source -ceq 'InlaidPathTransactionActive' -and $transactionSetter[0].Target -ceq '1') 'MSI private transaction-active property setter is missing or malformed.'
$expectedActionTypes = [ordered]@{ RollbackUserPath = 1282; ApplyUserPath = 1026; UninstallUserPath = 1026; FinalizeUserPathMarker = 1026; FailAfterUserPath = 1026; CommitUserPath = 1538 }
$expectedActionCommands = [ordered]@{ RollbackUserPath = '--action rollback'; ApplyUserPath = '--action apply'; UninstallUserPath = '--action uninstall'; FinalizeUserPathMarker = '--action finalize'; FailAfterUserPath = '--action fail'; CommitUserPath = '--action commit' }
foreach ($name in $expectedActionTypes.Keys) {
    $action = @($customActions | Where-Object { $_.Action -ceq $name })
    Require ($action.Count -eq 1 -and [int]$action[0].Type -eq $expectedActionTypes[$name] -and
        $action[0].Source -ceq 'InlaidPathHelper' -and
        $action[0].Target.Contains($expectedActionCommands[$name]) -and
        $action[0].Target.Contains('[TempFolder]inlaid-path-[ProductCode].json') -and
        -not $action[0].Target.Contains('inlaid-path-E1D7019B.json') -and
        -not $action[0].Target.Contains('[CustomActionData]')) "MSI custom action mismatch: $name"
    Require (([int]$action[0].Type -band 2048) -eq 0) "MSI custom action unexpectedly requests no-impersonate/elevated execution: $name"
}
Require (@($customActions | Where-Object { $_.Action -ceq 'FailAfterFinalizeUserPathMarker' }).Count -eq 0) 'MSI retains the unsafe deferred complete-uninstall failure hook.'
Require (@($customActions | Where-Object { $_.Action -ceq 'FailAfterRemoveExistingProducts' }).Count -eq 0) 'MSI retains the unsupported post-RemoveExistingProducts rollback test hook.'

$executeSequence = Read-MsiRows $database 'SELECT `Action`, `Condition`, `Sequence` FROM `InstallExecuteSequence`' @('Action', 'Condition', 'Sequence')
Require (@($executeSequence | Where-Object { $_.Action -ceq 'FailAfterFinalizeUserPathMarker' }).Count -eq 0) 'MSI retains an orphaned sequence row for the unsafe deferred complete-uninstall failure hook.'
Require (@($executeSequence | Where-Object { $_.Action -ceq 'FailAfterRemoveExistingProducts' }).Count -eq 0) 'MSI retains an orphaned post-RemoveExistingProducts rollback test hook.'
$executeCostFinalize = @($executeSequence | Where-Object { $_.Action -ceq 'CostFinalize' })
$executePathDirectory = @($executeSequence | Where-Object { $_.Action -ceq 'SetINLAID_PATH_PROGRAM_DIR' })
$executePreflight = @($executeSequence | Where-Object { $_.Action -ceq 'PreflightUserPathState' })
$executeTransactionSetter = @($executeSequence | Where-Object { $_.Action -ceq 'SetInlaidPathTransactionActive' })
$executeRollbackPath = @($executeSequence | Where-Object { $_.Action -ceq 'RollbackUserPath' })
$executeApplyPath = @($executeSequence | Where-Object { $_.Action -ceq 'ApplyUserPath' })
$executeUninstallPath = @($executeSequence | Where-Object { $_.Action -ceq 'UninstallUserPath' })
$executeFinalizeMarker = @($executeSequence | Where-Object { $_.Action -ceq 'FinalizeUserPathMarker' })
$executeFailAfterPath = @($executeSequence | Where-Object { $_.Action -ceq 'FailAfterUserPath' })
$executeRemoveRegistry = @($executeSequence | Where-Object { $_.Action -ceq 'RemoveRegistryValues' })
$executeRemoveExisting = @($executeSequence | Where-Object { $_.Action -ceq 'RemoveExistingProducts' })
$executeInstallFinalize = @($executeSequence | Where-Object { $_.Action -ceq 'InstallFinalize' })
$executeCommitPath = @($executeSequence | Where-Object { $_.Action -ceq 'CommitUserPath' })
Require ($executeCostFinalize.Count -eq 1 -and $executePathDirectory.Count -eq 1 -and
    [int]$executePathDirectory[0].Sequence -gt [int]$executeCostFinalize[0].Sequence) 'MSI execute sequence does not capture INSTALLFOLDER after costing.'
Require ($executeUninstallPath.Count -eq 1 -and $executeRemoveRegistry.Count -eq 1 -and
    [int]$executeUninstallPath[0].Sequence -lt [int]$executeRemoveRegistry[0].Sequence) 'MSI removes PATH provenance before the uninstall helper can consume it.'
Require ($executeFinalizeMarker.Count -eq 1 -and $executeRemoveRegistry.Count -eq 1 -and
    [int]$executeFinalizeMarker[0].Sequence -gt [int]$executeRemoveRegistry[0].Sequence -and
    [int]$executeFinalizeMarker[0].Sequence -lt [int]$executeCommitPath[0].Sequence -and
    $executeFinalizeMarker[0].Condition.Contains('REMOVE~="ALL"')) 'MSI does not verify finalized installer-private marker values after Registry-table removal and before commit.'
Require ($executePreflight.Count -eq 1 -and $executeTransactionSetter.Count -eq 1 -and $executeRollbackPath.Count -eq 1 -and
    $executePreflight[0].Condition -ceq 'NOT UPGRADINGPRODUCTCODE' -and
    $executeTransactionSetter[0].Condition -ceq 'NOT UPGRADINGPRODUCTCODE' -and
    [int]$executePreflight[0].Sequence -lt [int]$executeTransactionSetter[0].Sequence -and
    [int]$executeTransactionSetter[0].Sequence -lt [int]$executeRollbackPath[0].Sequence) 'Stale-state preflight and private transaction activation do not run explicitly before rollback scheduling or the upgrading-away package is not excluded.'
foreach ($action in @($executeRollbackPath, $executeApplyPath, $executeUninstallPath, $executeFinalizeMarker, $executeFailAfterPath, $executeCommitPath)) {
    Require ($action.Count -eq 1 -and $action[0].Condition.Contains('NOT UPGRADINGPRODUCTCODE') -and
        $action[0].Condition.Contains('InlaidPathTransactionActive="1"')) "MSI PATH cleanup action is not directly upgrade-excluded and transaction-conditioned: $($action[0].Action)"
}
Require ($executeRemoveExisting.Count -eq 1 -and $executeInstallFinalize.Count -eq 1 -and
    [int]$executeRemoveExisting[0].Sequence -gt [int]$executeInstallFinalize[0].Sequence) 'MSI must commit the incoming product before removing the older product.'

$upgradeRows = Read-MsiRows $database 'SELECT `UpgradeCode`, `VersionMin`, `VersionMax`, `Attributes`, `ActionProperty` FROM `Upgrade`' @('UpgradeCode', 'VersionMin', 'VersionMax', 'Attributes', 'ActionProperty')
Require (@($upgradeRows | Where-Object { $_.UpgradeCode -ceq $ExpectedUpgradeCode }).Count -ge 1) 'MSI Upgrade table does not use the stable UpgradeCode.'
Require (@($upgradeRows | Where-Object { ([int]$_.Attributes -band 4) -ne 0 }).Count -eq 0) 'MSI ignores old-product removal failures during major upgrade.'
$launchConditions = Read-MsiRows $database 'SELECT `Condition`, `Description` FROM `LaunchCondition`' @('Condition', 'Description')
Require (@($launchConditions | Where-Object { $_.Condition -ceq 'NOT RollbackDisabled' }).Count -eq 1) 'MSI LaunchCondition table does not reject rollback-disabled execution.'

$summary = $database.SummaryInformation(0)
$template = [string]$summary.Property(7)
$packageCode = [string]$summary.Property(9)
$wordCount = [int]$summary.Property(15)
$parsedPackage = [guid]::Empty
Require ($template -ceq 'x64;0') 'MSI Summary Information template is not x64.'
Require ([guid]::TryParse($packageCode, [ref]$parsedPackage)) 'MSI PackageCode is not a GUID.'
Require (($wordCount -band 2) -eq 2 -and ($wordCount -band 8) -eq 8) 'MSI Summary Information does not prove compressed, no-elevation packaging.'
Release-ComObject $summary
$summary = $null
Release-ComObject $database
$database = $null
Release-ComObject $installer
$installer = $null

$evidence = [ordered]@{
    schema = 2
    wixVersion = $wixVersion[0]
    msi = $Msi
    msiSHA256 = (Get-FileHash -LiteralPath $Msi -Algorithm SHA256).Hash
    wixPdbSHA256 = if (Test-Path -LiteralPath $pdb -PathType Leaf) { (Get-FileHash -LiteralPath $pdb -Algorithm SHA256).Hash } else { $null }
    validation = [ordered]@{
        exitCode = 0
        suppressed = @('ICE64')
        output = @($validationOutput | ForEach-Object { [string]$_ })
        expectedICE64 = [ordered]@{ exitCode = $ice64ExitCode; reportCount = $ice64Reports.Count; output = @($ice64Output | ForEach-Object { [string]$_ }) }
    }
    decompiledSHA256 = (Get-FileHash -LiteralPath $decompiled -Algorithm SHA256).Hash
    identity = [ordered]@{ productCode = $productCode; upgradeCode = $ExpectedUpgradeCode; packageCode = $packageCode; productVersion = $ExpectedVersion }
    summary = [ordered]@{ template = $template; wordCount = $wordCount; noElevationBit = (($wordCount -band 8) -eq 8) }
    scope = [ordered]@{ package = 'perUser'; allUsersPresent = $propertyMap.ContainsKey('ALLUSERS'); msiInstallPerUserPresent = $propertyMap.ContainsKey('MSIINSTALLPERUSER'); directory = 'LocalAppDataFolder\Programs\Inlaid' }
    architecture = [ordered]@{ package = 'x64'; application = $applicationPe; pathHelper = [ordered]@{ pe = $helperPe; testHooks = $helperProbe } }
    payload = $payloadEvidence
    tables = @($tables | Sort-Object)
    components = @($components | Sort-Object Component)
    files = @($fileEvidence | Sort-Object file)
    registry = @($registry | Sort-Object Component)
    customActions = @($customActions | Sort-Object Action)
    installExecuteSequence = @($executeSequence | Sort-Object { [int]$_.Sequence })
    launchConditions = @($launchConditions | Sort-Object Condition)
    removeFolders = @($removeFolders | Sort-Object Directory)
}
$evidencePath = Join-Path $EvidenceDirectory 'msi-evidence.json'
$evidence | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $evidencePath -Encoding utf8NoBOM
Write-Host "Validated and decompiled strict per-user MSI: $Msi" -ForegroundColor Green
Write-Host "Evidence: $evidencePath"
