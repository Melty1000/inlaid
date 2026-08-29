[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Wix,
    [switch]$AcceptInstall
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $AcceptInstall) {
    throw 'This route temporarily mutates the current-user PATH and installs/removes per-user MSI test packages. Re-run with -AcceptInstall only in an authorized disposable Windows environment.'
}

$ProjectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$LifecycleEvidenceRoot = Join-Path $ProjectRoot '.tools\evidence\windows-msi-lifecycle'
$EvidenceRunName = 'run ' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + ' ' + [Guid]::NewGuid().ToString('N').Substring(0, 8)
$RetainedLifecycleEvidence = Join-Path $LifecycleEvidenceRoot $EvidenceRunName
$ScriptStartedUtc = [DateTime]::UtcNow
$InitializationPhase = 'bootstrap'
$LifecycleFailure = $null
$LifecyclePassed = $false
$CleanupErrors = @()
$CleanupSuppressedReason = ''
$MsiClientTimedOut = $false
$MsiClientTimeoutSeconds = 360
$MsiClientRecords = @()
$MsiInvocationCounter = 0
$LifecycleMutationStarted = $false
$LifecycleEvidenceRetained = $false
$RetainedHelperDiagnosticPaths = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
$OriginalProcessPath = $env:PATH
$OriginalPathCaptured = $false
$EnvironmentKey = $null
$TemporaryBase = ''
$TemporaryRoot = ''
$LifecycleSnapshotDirectory = ''
$LocalAppData = ''
$Videos = ''
$Pictures = ''
$Documents = ''
$RoamingAppData = ''
$InstallRoot = ''
$StartMenu = ''
$SettingsRoot = ''
$UserData = @()
$InstalledMsi = ''
$ExpectedUserDataSnapshot = ''
$First = ''
$Second = ''
$FirstEvidenceDirectory = ''
$SecondEvidenceDirectory = ''
$LifecycleProductCodes = @()

function Limit-EvidenceText([object[]]$Lines, [int]$MaximumCharacters = 65536) {
    $text = (@($Lines | ForEach-Object { [string]$_ }) -join [Environment]::NewLine)
    if ($text.Length -le $MaximumCharacters) { return $text }
    return $text.Substring(0, $MaximumCharacters) + [Environment]::NewLine + '<truncated>'
}

function Get-BoundedCommandEvidence([string]$FilePath, [string[]]$Arguments) {
    try {
        $output = @(& $FilePath @Arguments 2>&1)
        return [ordered]@{ command = $FilePath; arguments = $Arguments; exitCode = $LASTEXITCODE; output = Limit-EvidenceText $output }
    }
    catch {
        return [ordered]@{ command = $FilePath; arguments = $Arguments; error = $_.Exception.Message }
    }
}

function Get-RunnerEvidence {
    $identityEvidence = $null
    try {
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
        $principal = New-Object Security.Principal.WindowsPrincipal($identity)
        $identityEvidence = [ordered]@{
            name = $identity.Name
            ownerSid = [string]$identity.Owner
            impersonationLevel = [string]$identity.ImpersonationLevel
            administratorRoleEnabledInCurrentToken = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
            standardUserProven = $false
            note = 'Current-token administrator-role state and whoami output are observations; an elevated runner is not standard-user proof.'
        }
    }
    catch { $identityEvidence = [ordered]@{ error = $_.Exception.Message; standardUserProven = $false } }
    return [ordered]@{
        runner = [ordered]@{
            os = $env:RUNNER_OS; architecture = $env:RUNNER_ARCH; imageOS = $env:ImageOS; imageVersion = $env:ImageVersion
            environment = $env:GITHUB_ACTIONS; machine = $env:COMPUTERNAME
            repository = $env:GITHUB_REPOSITORY; workflow = $env:GITHUB_WORKFLOW; job = $env:GITHUB_JOB
            runId = $env:GITHUB_RUN_ID; runAttempt = $env:GITHUB_RUN_ATTEMPT; sha = $env:GITHUB_SHA; ref = $env:GITHUB_REF
        }
        windows = [ordered]@{
            version = [Environment]::OSVersion.VersionString
            is64BitOperatingSystem = [Environment]::Is64BitOperatingSystem
            is64BitProcess = [Environment]::Is64BitProcess
        }
        powershell = [ordered]@{
            edition = $PSVersionTable.PSEdition; version = [string]$PSVersionTable.PSVersion
            processArchitecture = $env:PROCESSOR_ARCHITECTURE
        }
        identity = $identityEvidence
        token = Get-BoundedCommandEvidence 'whoami.exe' @('/all')
        dotnet = Get-BoundedCommandEvidence 'dotnet.exe' @('--info')
        wix = Get-BoundedCommandEvidence $Wix @('--version')
    }
}

function Write-LifecycleStateEvidence([string]$Phase, $FailureRecord) {
    $states = Join-Path $RetainedLifecycleEvidence 'states'
    New-Item -ItemType Directory -Force -Path $states | Out-Null
    $state = [ordered]@{
        schema = 1
        phase = $Phase
        createdUtc = [DateTime]::UtcNow.ToString('o')
        initializationPhase = $InitializationPhase
        lifecyclePassed = $LifecyclePassed
        cleanupErrors = @($CleanupErrors)
        failure = if ($null -eq $FailureRecord) { $null } else { [ordered]@{
            message = $FailureRecord.Exception.Message
            category = [string]$FailureRecord.CategoryInfo
            fullyQualifiedErrorId = $FailureRecord.FullyQualifiedErrorId
            scriptStackTrace = $FailureRecord.ScriptStackTrace
        } }
        msiClient = [ordered]@{
            timeoutSeconds = $MsiClientTimeoutSeconds
            timedOut = $MsiClientTimedOut
            serviceSettled = if ($MsiClientTimedOut) { $false } else { $null }
            cleanupSuppressedReason = $CleanupSuppressedReason
            records = @($MsiClientRecords)
        }
        paths = [ordered]@{
            temporaryRoot = $TemporaryRoot
            installRoot = $InstallRoot
            installRootPresent = (-not [string]::IsNullOrWhiteSpace($InstallRoot) -and (Test-Path -LiteralPath $InstallRoot))
            originalPathCaptured = $OriginalPathCaptured
            lifecycleMutationStarted = $LifecycleMutationStarted
        }
        runner = Get-RunnerEvidence
    }
    foreach ($probe in @(
        @('path', 'Get-ExactUserPathState'),
        @('marker', 'Get-MarkerSnapshot'),
        @('installerStructure', 'Get-InstallerRegistryStructureSnapshot'),
        @('installTree', 'Get-InstallTreeSnapshot'),
        @('registration', 'Get-InlaidRegistrationSnapshot'),
        @('transactionFiles', 'Get-TransactionFilesSnapshot'),
        @('userData', 'Get-ExistingUserDataEvidence')
    )) {
        $name, $command = $probe
        if (-not $LifecycleMutationStarted -and $name -cne 'path') {
            $state[$name] = [ordered]@{ unavailable = $true; reason = 'Lifecycle mutation did not start; potentially foreign pre-existing state was not recursively inventoried.' }
            continue
        }
        if ($null -eq (Get-Command $command -CommandType Function -ErrorAction SilentlyContinue)) {
            $state[$name] = [ordered]@{ unavailable = $true; reason = 'Initialization did not reach this evidence function.' }
            continue
        }
        try {
            $state[$name] = if ($command -ceq 'Get-TransactionFilesSnapshot') { & $command $LifecycleProductCodes } else { & $command }
        }
        catch { $state[$name] = [ordered]@{ error = $_.Exception.Message } }
    }
    $safePhase = $Phase -replace '[^A-Za-z0-9._-]', '-'
    $state | ConvertTo-Json -Depth 16 | Set-Content -LiteralPath (Join-Path $states ($safePhase + '.json')) -Encoding utf8NoBOM
    [ordered]@{
        schema = 1; phase = $Phase; createdUtc = [DateTime]::UtcNow.ToString('o')
        lifecyclePassed = $LifecyclePassed; initializationPhase = $InitializationPhase
        msiClientTimedOut = $MsiClientTimedOut; cleanupSuppressedReason = $CleanupSuppressedReason
    } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $RetainedLifecycleEvidence 'bootstrap.json') -Encoding utf8NoBOM
}

try {
    $InitializationPhase = 'repository initialization'
    . (Join-Path $PSScriptRoot 'resolve-payload.ps1')
    $BuildScript = Join-Path $PSScriptRoot 'build-windows-msi.ps1'
    $InspectScript = Join-Path $PSScriptRoot 'inspect-windows-msi.ps1'
    $TemporaryBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    $TemporaryRoot = [System.IO.Path]::GetFullPath((Join-Path $TemporaryBase ('inlaid msi test ' + [Guid]::NewGuid().ToString('N'))))
    $LifecycleSnapshotDirectory = Join-Path $TemporaryRoot 'snapshots'
    $InitializationPhase = 'known-folder resolution'
    $LocalAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    $Videos = [Environment]::GetFolderPath([Environment+SpecialFolder]::MyVideos)
    $Pictures = [Environment]::GetFolderPath([Environment+SpecialFolder]::MyPictures)
    $Documents = [Environment]::GetFolderPath([Environment+SpecialFolder]::MyDocuments)
    $RoamingAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::ApplicationData)
    foreach ($knownFolder in @($LocalAppData, $Videos, $Pictures, $Documents, $RoamingAppData)) {
        if ([string]::IsNullOrWhiteSpace($knownFolder) -or -not [System.IO.Path]::IsPathRooted($knownFolder)) {
            throw 'A required current-user known folder could not be resolved.'
        }
    }
    $InstallRoot = Join-Path $LocalAppData 'Programs\Inlaid'
$MarkerRegistryPath = 'HKCU:\Software\Inlaid\Installer'
$MarkerRegistrySubKey = 'Software\Inlaid\Installer'
$KnownMarkerValueNames = @('Schema', 'NormalizedProgramDirectory', 'InsertedSegment', 'Owned', 'PathValueExistedBeforeOwnership', 'Component')
$KnownComponentValueNames = @('InlaidExecutable', 'InlaidReadme', 'InlaidLicense', 'InlaidNotices', 'InlaidFilterDocumentation', 'InlaidFilterReadme')
    $StartMenu = Join-Path $RoamingAppData 'Microsoft\Windows\Start Menu\Programs\Inlaid\Inlaid.lnk'
    $SettingsRoot = Join-Path $LocalAppData 'Inlaid'
    $UserData = @(
    (Join-Path $SettingsRoot 'inlaid-settings.json'),
    (Join-Path $SettingsRoot 'Recovery\inlaid-test.celltape'),
    (Join-Path $Videos 'Inlaid\inlaid-test-recording.txt'),
    (Join-Path $Pictures 'Inlaid\inlaid-test-snapshot.txt'),
    (Join-Path $Documents 'Inlaid\Filters\inlaid-test-filter.cube'),
    (Join-Path $Documents 'Inlaid\Support Reports\inlaid-test-report.json')
    )
$UpgradeCode = '{E1D7019B-07CD-4A8E-8D65-C6FA20B7F07D}'
$ComponentCodes = @(
    '{882B8192-1A62-4B36-884A-4227DBA1DD7B}', '{F2503590-7052-453B-A7DA-D31BD1426CC7}',
    '{B1132EB7-D31B-40E1-BBC3-04F0E1AB4BF4}', '{873F13DF-4A0F-4EA3-A09C-8D22BFE35667}',
    '{55C8F27E-9D2F-4DDD-AE65-1EFAB7B43B42}', '{12CF70D6-D308-4D00-A2A1-1F818A67ED75}',
    '{BA72C6B8-2ECE-4908-8D38-D17959D78636}'
)
$LifecycleSnapshotDirectory = Join-Path $TemporaryRoot 'snapshots'
$NativeRegistrySource = @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

public sealed class InlaidRawRegistryValue {
    public bool Present;
    public UInt32 Type;
    public byte[] Data;
}

public static class InlaidNativeUserPath {
    private static readonly IntPtr HKEY_CURRENT_USER = new IntPtr(unchecked((int)0x80000001));
    private const int ERROR_SUCCESS = 0;
    private const int ERROR_FILE_NOT_FOUND = 2;
    private const int ERROR_MORE_DATA = 234;
    private const UInt32 KEY_QUERY_VALUE = 0x0001;
    private const UInt32 KEY_SET_VALUE = 0x0002;
    private const int MaxValueBytes = 1024 * 1024;

    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern int RegOpenKeyExW(IntPtr key, string subkey, UInt32 options, UInt32 access, out IntPtr result);
    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern int RegQueryValueExW(IntPtr key, string name, IntPtr reserved, out UInt32 type, byte[] data, ref UInt32 size);
    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern int RegSetValueExW(IntPtr key, string name, UInt32 reserved, UInt32 type, byte[] data, UInt32 size);
    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern int RegDeleteValueW(IntPtr key, string name);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern int RegCloseKey(IntPtr key);

    private static IntPtr Open(UInt32 access) {
        IntPtr key;
        int result = RegOpenKeyExW(HKEY_CURRENT_USER, "Environment", 0, access, out key);
        if (result != ERROR_SUCCESS) throw new Win32Exception(result, "Open HKCU\\Environment");
        return key;
    }

    public static InlaidRawRegistryValue Read() {
        IntPtr key = Open(KEY_QUERY_VALUE);
        try {
            for (int attempt = 0; attempt != 4; attempt++) {
                UInt32 type;
                UInt32 size = 0;
                int result = RegQueryValueExW(key, "Path", IntPtr.Zero, out type, null, ref size);
                if (result == ERROR_FILE_NOT_FOUND) return new InlaidRawRegistryValue { Present = false, Type = 0, Data = new byte[0] };
                if (result != ERROR_SUCCESS && result != ERROR_MORE_DATA) throw new Win32Exception(result, "Size HKCU\\Environment\\Path");
                if (size > MaxValueBytes) throw new InvalidOperationException("HKCU\\Environment\\Path exceeds the bounded evidence limit.");
                byte[] data = new byte[checked((int)size)];
                UInt32 actual = size;
                result = RegQueryValueExW(key, "Path", IntPtr.Zero, out type, data, ref actual);
                if (result == ERROR_MORE_DATA) continue;
                if (result != ERROR_SUCCESS) throw new Win32Exception(result, "Read HKCU\\Environment\\Path");
                if (actual != data.Length) Array.Resize(ref data, checked((int)actual));
                return new InlaidRawRegistryValue { Present = true, Type = type, Data = data };
            }
            throw new InvalidOperationException("HKCU\\Environment\\Path changed during bounded native read.");
        }
        finally { RegCloseKey(key); }
    }

    public static void Write(UInt32 type, byte[] data) {
        if (type != 1 && type != 2) throw new ArgumentOutOfRangeException("type", "PATH evidence permits only REG_SZ or REG_EXPAND_SZ.");
        if (data == null || data.Length > MaxValueBytes) throw new ArgumentOutOfRangeException("data", "PATH evidence bytes exceed the bounded limit.");
        IntPtr key = Open(KEY_SET_VALUE);
        try {
            int result = RegSetValueExW(key, "Path", 0, type, data, checked((UInt32)data.Length));
            if (result != ERROR_SUCCESS) throw new Win32Exception(result, "Write HKCU\\Environment\\Path");
        }
        finally { RegCloseKey(key); }
    }

    public static void Delete() {
        IntPtr key = Open(KEY_SET_VALUE);
        try {
            int result = RegDeleteValueW(key, "Path");
            if (result != ERROR_SUCCESS && result != ERROR_FILE_NOT_FOUND) throw new Win32Exception(result, "Delete HKCU\\Environment\\Path");
        }
        finally { RegCloseKey(key); }
    }
}
'@
$InitializationPhase = 'native registry helper compilation'
Add-Type -TypeDefinition $NativeRegistrySource -Language CSharp

function ConvertTo-Hex([byte[]]$Bytes) {
    if ($null -eq $Bytes -or $Bytes.Length -eq 0) { return '' }
    return ([BitConverter]::ToString($Bytes)).Replace('-', '').ToLowerInvariant()
}

function ConvertTo-RegistryStringBytes([string]$Value) {
    return [Text.Encoding]::Unicode.GetBytes($Value + [char]0)
}

function Set-RawUserPath([uint32]$Type, [byte[]]$Bytes) {
    [InlaidNativeUserPath]::Write($Type, $Bytes)
}

$InitializationPhase = 'native registry PATH capture'
$InitialNativePath = [InlaidNativeUserPath]::Read()
$EnvironmentKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if ($null -eq $EnvironmentKey) { throw 'Current-user Environment registry key is unavailable.' }
$PathPresent = $InitialNativePath.Present
$OriginalPath = if ($PathPresent) { [string]$EnvironmentKey.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) } else { '' }
$OriginalPathType = if ($PathPresent) { [uint32]$InitialNativePath.Type } else { [uint32]2 }
$OriginalPathBytes = if ($PathPresent) { [byte[]]$InitialNativePath.Data.Clone() } else { [byte[]]@() }
$OriginalPathKind = if ($PathPresent) { [Microsoft.Win32.RegistryValueKind]$OriginalPathType } else { [Microsoft.Win32.RegistryValueKind]::ExpandString }
$OwnedPathKind = if ($PathPresent) { $OriginalPathKind } else { [Microsoft.Win32.RegistryValueKind]::ExpandString }
$OriginalPathCaptured = $true

function Set-ExactUserPath([string]$Value) {
    Set-RawUserPath ([uint32]$OriginalPathKind) (ConvertTo-RegistryStringBytes $Value)
}

function Get-ExactUserPath {
    return [string](Get-ExactUserPathState).Raw
}

function Get-ExactUserPathState {
    $native = [InlaidNativeUserPath]::Read()
    $present = $native.Present
    $decoded = ''
    if ($present) {
        try { $decoded = [string]$EnvironmentKey.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) }
        catch { $decoded = $null }
    }
    return [pscustomobject]@{
        Present = $present
        Raw = $decoded
        Kind = if ($present) { [Microsoft.Win32.RegistryValueKind]$native.Type } else { $null }
        Type = if ($present) { [uint32]$native.Type } else { $null }
        RawBytes = if ($present) { [byte[]]$native.Data } else { [byte[]]@() }
        RawHex = if ($present) { ConvertTo-Hex ([byte[]]$native.Data) } else { '' }
    }
}

function Get-ExactUserPathSnapshot {
    return (Get-ExactUserPathState | ConvertTo-Json -Compress)
}

function Assert-ExactUserPathState([bool]$ExpectedPresent, [string]$ExpectedRaw, $ExpectedKind, [string]$Context) {
    $actual = Get-ExactUserPathState
    $kindMatches = if ($ExpectedPresent) { [string]$actual.Kind -ceq [string]$ExpectedKind } else { $null -eq $actual.Kind }
    $expectedHex = if ($ExpectedPresent) { ConvertTo-Hex (ConvertTo-RegistryStringBytes $ExpectedRaw) } else { '' }
    if ($actual.Present -ne $ExpectedPresent -or $actual.Raw -cne $ExpectedRaw -or -not $kindMatches -or $actual.RawHex -cne $expectedHex) {
        throw "$Context user PATH state mismatch. Expected present=$ExpectedPresent raw='$ExpectedRaw' kind='$ExpectedKind'; got $($actual | ConvertTo-Json -Compress)."
    }
}

function Assert-RawUserPathState([bool]$ExpectedPresent, [uint32]$ExpectedType, [byte[]]$ExpectedBytes, [string]$Context) {
    $actual = Get-ExactUserPathState
    $expectedHex = if ($ExpectedPresent) { ConvertTo-Hex $ExpectedBytes } else { '' }
    if ($actual.Present -ne $ExpectedPresent -or
        ($ExpectedPresent -and ($actual.Type -ne $ExpectedType -or $actual.RawHex -cne $expectedHex))) {
        throw "$Context raw user PATH mismatch. Expected present=$ExpectedPresent type=$ExpectedType hex=$expectedHex; got present=$($actual.Present) type=$($actual.Type) hex=$($actual.RawHex)."
    }
}

function Assert-OriginalUserPath([string]$Context) {
    Assert-RawUserPathState $PathPresent $OriginalPathType $OriginalPathBytes $Context
    if ($PathPresent) {
        $actual = Get-ExactUserPathState
        if ($actual.Raw -cne $OriginalPath) { throw "$Context decoded user PATH mismatch." }
    }
}

function Restore-OriginalUserPath {
    if ($PathPresent) { Set-RawUserPath $OriginalPathType $OriginalPathBytes }
    else { [InlaidNativeUserPath]::Delete() }
}

function Get-InstallTreeSnapshot {
    if (-not (Test-Path -LiteralPath $InstallRoot -PathType Container)) { return '<absent>' }
    $rows = @(Get-ChildItem -LiteralPath $InstallRoot -File -Recurse | Sort-Object FullName | ForEach-Object {
        [ordered]@{
            path = $_.FullName.Substring($InstallRoot.Length + 1)
            length = $_.Length
            sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
        }
    })
    return ($rows | ConvertTo-Json -Depth 3 -Compress)
}

function Get-EvidenceFileState([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return [ordered]@{ present = $false }
    }
    try {
        $file = Get-Item -LiteralPath $Path
        return [ordered]@{
            present = $true; length = $file.Length; lastWriteUtc = $file.LastWriteTimeUtc.ToString('o')
            sha256 = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash
        }
    }
    catch { return [ordered]@{ present = $true; error = $_.Exception.Message } }
}

function Write-MsiClientRecord([string]$Name, [object]$Record) {
    if ([string]::IsNullOrWhiteSpace($TemporaryRoot)) { return }
    $directory = Join-Path $TemporaryRoot 'msi-clients'
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    $Record | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $directory ($Name + '.json')) -Encoding utf8NoBOM
}

function Invoke-MsiExitCode([string[]]$Arguments, [string]$LogName) {
    $log = Join-Path $TemporaryRoot $LogName
    $allArguments = @($Arguments) + @('/norestart', '/l*v', $log)
    $quotedArguments = @($allArguments | ForEach-Object {
        $value = [string]$_
        if ($value -match '[\s"]') { '"' + $value.Replace('"', '\"') + '"' }
        else { $value }
    })
    $script:MsiInvocationCounter++
    $recordName = ('{0:D3}-{1}' -f $MsiInvocationCounter, ($LogName -replace '[^A-Za-z0-9._-]', '-'))
    $record = [ordered]@{
        schema = 1; name = $recordName; startedUtc = [DateTime]::UtcNow.ToString('o')
        timeoutSeconds = $MsiClientTimeoutSeconds; arguments = @($Arguments); log = $log
        processId = $null; status = 'starting'; exitCode = $null; logState = Get-EvidenceFileState $log
        windowsInstallerServiceSettled = $null
    }
    Write-MsiClientRecord $recordName $record
    $process = Start-Process -FilePath 'msiexec.exe' -ArgumentList $quotedArguments -WindowStyle Hidden -PassThru
    $record.processId = $process.Id
    $record.status = 'running'
    Write-MsiClientRecord $recordName $record
    if (-not $process.WaitForExit($MsiClientTimeoutSeconds * 1000)) {
        $script:MsiClientTimedOut = $true
        $script:CleanupSuppressedReason = "MSI client PID $($process.Id) timed out; Windows Installer service completion is unknown, so lifecycle cleanup mutations are suppressed."
        $record.status = 'timed-out-before-client-stop'
        $record.timedOutUtc = [DateTime]::UtcNow.ToString('o')
        $record.logState = Get-EvidenceFileState $log
        $record.windowsInstallerServiceSettled = $false
        $script:MsiClientRecords += [pscustomobject]$record
        Write-MsiClientRecord $recordName $record
        try { [void](Retain-LifecycleEvidence 'failed-timeout' 'timeout-pre-client-stop') }
        catch { $script:CleanupErrors += "timeout pre-stop artifact retention: $($_.Exception.Message)" }
        try { Write-LifecycleStateEvidence 'timeout-pre-client-stop' $null }
        catch { $script:CleanupErrors += "timeout pre-stop state evidence: $($_.Exception.Message)" }
        try { [void](Retain-LifecycleEvidence 'failed-timeout' 'timeout-pre-client-stop-state') }
        catch { $script:CleanupErrors += "timeout pre-stop state retention: $($_.Exception.Message)" }
        try {
            $process.Kill()
            [void]$process.WaitForExit(10000)
            $record.status = if ($process.HasExited) { 'timed-out-client-stopped' } else { 'timed-out-client-stop-unconfirmed' }
        }
        catch {
            $record.status = 'timed-out-client-stop-failed'
            $record.stopError = $_.Exception.Message
        }
        $record.clientStopObservedUtc = [DateTime]::UtcNow.ToString('o')
        $record.logStateAfterClientStop = Get-EvidenceFileState $log
        Write-MsiClientRecord $recordName $record
        try { [void](Retain-LifecycleEvidence 'failed-timeout' 'timeout-post-client-stop') }
        catch { $script:CleanupErrors += "timeout post-stop artifact retention: $($_.Exception.Message)" }
        throw "Windows Installer client PID $($process.Id) exceeded the per-call timeout of $MsiClientTimeoutSeconds seconds. The exact client stop was requested; Windows Installer service state remains unknown."
    }
    $record.status = 'exited'
    $record.completedUtc = [DateTime]::UtcNow.ToString('o')
    $record.exitCode = $process.ExitCode
    $record.logState = Get-EvidenceFileState $log
    $script:MsiClientRecords += [pscustomobject]$record
    Write-MsiClientRecord $recordName $record
    return $process.ExitCode
}

function Invoke-Msi([string[]]$Arguments, [string]$Description, [string]$LogName) {
    $exitCode = Invoke-MsiExitCode $Arguments $LogName
    if ($exitCode -ne 0) { throw "$Description failed with Windows Installer exit code $exitCode. See $LogName." }
}

function Assert-FailedMsi([string[]]$Arguments, [string]$Description, [string]$LogName) {
    $exitCode = Invoke-MsiExitCode $Arguments $LogName
    if ($exitCode -eq 0) { throw "$Description unexpectedly succeeded." }
}

function Assert-ActionFailureLog([string]$LogName, [string]$Action, [string]$ExpectedMessage = '') {
    $path = Join-Path $TemporaryRoot $LogName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Expected-failure MSI log is missing: $path" }
    $text = Get-Content -LiteralPath $path -Raw
    $escaped = [regex]::Escape($Action)
    $immediateStart = [regex]::Match($text, "(?m)^Action start .*: $escaped\.\s*$")
    $immediateFailure = [regex]::Match($text, "(?m)^Action ended .*: $escaped\. Return value 3\.\s*$")
    $startIndex = -1
    $failureIndex = -1
    if ($immediateStart.Success -and $immediateFailure.Success -and $immediateFailure.Index -gt $immediateStart.Index) {
        $startIndex = $immediateStart.Index
        $failureIndex = $immediateFailure.Index
    }
    else {
        # Deferred executable custom actions are represented inside the execute
        # script rather than by the immediate "Action start/ended" pair.
        $deferredStart = [regex]::Match($text, "(?m)^MSI .*Executing op: ActionStart\(Name=$escaped(?:,|\))")
        $deferredSchedule = [regex]::Match($text, "(?m)^MSI .*Executing op: CustomActionSchedule\(Action=$escaped,")
        $deferredFailure = [regex]::Match($text, "(?m)^CustomAction $escaped returned actual error code (?!0(?:\s|$))\d+")
        if ($deferredStart.Success -and $deferredSchedule.Success -and $deferredFailure.Success -and
            $deferredSchedule.Index -gt $deferredStart.Index -and $deferredFailure.Index -gt $deferredSchedule.Index) {
            $startIndex = $deferredStart.Index
            $failureIndex = $deferredFailure.Index
        }
    }
    $messageIndex = if ([string]::IsNullOrWhiteSpace($ExpectedMessage)) { $startIndex } else { $text.IndexOf($ExpectedMessage, [System.StringComparison]::Ordinal) }
    if ($startIndex -lt 0 -or $failureIndex -le $startIndex -or
        $messageIndex -lt $startIndex -or $messageIndex -gt $failureIndex) {
        throw "MSI log does not prove intended action $Action caused the failure: $LogName"
    }
}

function Assert-ActionSuccessLog([string]$LogName, [string]$Action) {
    $path = Join-Path $TemporaryRoot $LogName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Successful MSI log is missing: $path" }
    $text = Get-Content -LiteralPath $path -Raw
    $escaped = [regex]::Escape($Action)
    $start = [regex]::Match($text, "(?m)^Action start .*: $escaped\.\s*$")
    $success = [regex]::Match($text, "(?m)^Action ended .*: $escaped\. Return value 1\.\s*$")
    $deferredStart = [regex]::Match($text, "(?m)^MSI .*Executing op: ActionStart\(Name=$escaped(?:,|\))")
    $deferredSchedule = [regex]::Match($text, "(?m)^MSI .*Executing op: CustomActionSchedule\(Action=$escaped,")
    $installFinalizeSuccess = @([regex]::Matches($text, '(?m)^Action ended .*: InstallFinalize\. Return value 1\.\s*$') |
        Where-Object { $_.Index -gt $deferredSchedule.Index } | Select-Object -First 1)
    $deferredFailure = [regex]::Match($text, "(?m)^CustomAction $escaped returned actual error code (?!0(?:\s|$))\d+")
    $immediateProven = $start.Success -and $success.Success -and $success.Index -gt $start.Index
    $deferredProven = $deferredStart.Success -and $deferredSchedule.Success -and $installFinalizeSuccess.Count -eq 1 -and
        $deferredSchedule.Index -gt $deferredStart.Index -and $installFinalizeSuccess[0].Index -gt $deferredSchedule.Index -and
        (-not $deferredFailure.Success -or $deferredFailure.Index -gt $installFinalizeSuccess[0].Index)
    if (-not $immediateProven -or -not $deferredProven) {
        throw "MSI log does not prove intended action $Action completed successfully: $LogName"
    }
}

function Assert-BasicUiLog([string]$LogName) {
    $path = Join-Path $TemporaryRoot $LogName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Basic-UI MSI log is missing: $path" }
    $text = Get-Content -LiteralPath $path -Raw
    if ($text -notmatch '(?mi)(?:Property\([^\r\n]*\):\s*UILevel\s*=\s*3\s*$|UILevel property\. Its value is ''3'')') {
        throw "MSI log does not prove Windows Installer basic UI level 3: $LogName"
    }
}

function Assert-RollbackDisabledLog([string]$LogName) {
    $path = Join-Path $TemporaryRoot $LogName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Rollback-disabled MSI log is missing: $path" }
    $text = Get-Content -LiteralPath $path -Raw
    $start = [regex]::Match($text, '(?m)^Action start .*: LaunchConditions\.\s*$')
    $message = 'Inlaid requires Windows Installer rollback to protect the current-user PATH.'
    $messageIndex = $text.IndexOf($message, [System.StringComparison]::Ordinal)
    $failure = [regex]::Match($text, '(?m)^Action ended .*: LaunchConditions\. Return value 3\.\s*$')
    if (-not $start.Success -or $messageIndex -lt $start.Index -or -not $failure.Success -or $failure.Index -lt $messageIndex) {
        throw 'Rollback-disabled log does not prove the package LaunchConditions action emitted the rollback requirement and failed.'
    }
}

function Assert-PostRemoveExistingProductsFailureLog([string]$LogName) {
    $path = Join-Path $TemporaryRoot $LogName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Post-RemoveExistingProducts MSI log is missing: $path" }
    $text = Get-Content -LiteralPath $path -Raw
    $removeCompleted = [regex]::Match($text, '(?m)^Action ended .*: RemoveExistingProducts\. Return value 1\.\s*$')
    $failureStarted = [regex]::Match($text, '(?m)^MSI .*Executing op: ActionStart\(Name=FailAfterRemoveExistingProducts(?:,|\))')
    $failureScheduled = [regex]::Match($text, '(?m)^MSI .*Executing op: CustomActionSchedule\(Action=FailAfterRemoveExistingProducts,')
    $failureReturned = [regex]::Match($text, '(?m)^CustomAction FailAfterRemoveExistingProducts returned actual error code (?!0(?:\s|$))\d+')
    if (-not $removeCompleted.Success -or -not $failureStarted.Success -or -not $failureScheduled.Success -or -not $failureReturned.Success -or
        $removeCompleted.Index -ge $failureStarted.Index -or $failureStarted.Index -ge $failureScheduled.Index -or
        $failureScheduled.Index -ge $failureReturned.Index) {
        throw 'Failed-upgrade log does not prove RemoveExistingProducts completed before FailAfterRemoveExistingProducts executed and failed.'
    }
}

function Assert-PostFinalizeUserPathMarkerFailureLog([string]$LogName) {
    $path = Join-Path $TemporaryRoot $LogName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Post-finalize MSI log is missing: $path" }
    $text = Get-Content -LiteralPath $path -Raw
    $finalizeStarted = [regex]::Match($text, '(?m)^MSI .*Executing op: ActionStart\(Name=FinalizeUserPathMarker(?:,|\))')
    $finalizeScheduled = [regex]::Match($text, '(?m)^MSI .*Executing op: CustomActionSchedule\(Action=FinalizeUserPathMarker,')
    $failureStarted = [regex]::Match($text, '(?m)^MSI .*Executing op: ActionStart\(Name=FailAfterFinalizeUserPathMarker(?:,|\))')
    $failureScheduled = [regex]::Match($text, '(?m)^MSI .*Executing op: CustomActionSchedule\(Action=FailAfterFinalizeUserPathMarker,')
    $failureReturned = [regex]::Match($text, '(?m)^CustomAction FailAfterFinalizeUserPathMarker returned actual error code (?!0(?:\s|$))\d+')
    $installExecuteStarted = [regex]::Match($text, '(?m)^Action start .*: InstallExecute\.\s*$')
    if (-not $finalizeStarted.Success -or -not $finalizeScheduled.Success -or -not $failureStarted.Success -or
        -not $failureScheduled.Success -or -not $failureReturned.Success -or -not $installExecuteStarted.Success -or
        $installExecuteStarted.Index -ge $finalizeStarted.Index -or
        $finalizeStarted.Index -ge $finalizeScheduled.Index -or $finalizeScheduled.Index -ge $failureStarted.Index -or
        $failureStarted.Index -ge $failureScheduled.Index -or $failureScheduled.Index -ge $failureReturned.Index) {
        throw 'Failed-uninstall log does not prove marker finalization completed before the test-only action executed and failed.'
    }
    $beforeFailure = $text.Substring(0, $failureReturned.Index)
    if ($beforeFailure -match '(?m)^MSI .*Executing op: (?:ProductUnregister|ProductUnpublish|SourceListUnpublish|ActionStart\(Name=(?:ProcessComponents|UnpublishFeatures)(?:,|\)))') {
        throw 'Failed-uninstall log shows Windows Installer registration teardown before the injected rollback checkpoint.'
    }
    if ($beforeFailure -match '(?m)^Action start .*: InstallFinalize\.\s*$') {
        throw 'Failed-uninstall log reached InstallFinalize before the injected rollback checkpoint.'
    }
}

function Assert-SplitUninstallRegistrationLog([string]$LogName) {
    $path = Join-Path $TemporaryRoot $LogName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Successful uninstall MSI log is missing: $path" }
    $text = Get-Content -LiteralPath $path -Raw
    $finalizeScheduled = [regex]::Match($text, '(?m)^MSI .*Executing op: CustomActionSchedule\(Action=FinalizeUserPathMarker,')
    $installExecuteCompleted = [regex]::Match($text, '(?m)^Action ended .*: InstallExecute\. Return value 1\.\s*$')
    $installFinalizeStarted = [regex]::Match($text, '(?m)^Action start .*: InstallFinalize\.\s*$')
    $productUnregistered = [regex]::Match($text, '(?m)^MSI .*Executing op: ProductUnregister\(')
    if (-not $finalizeScheduled.Success -or -not $installExecuteCompleted.Success -or -not $installFinalizeStarted.Success -or
        -not $productUnregistered.Success -or $finalizeScheduled.Index -ge $installExecuteCompleted.Index -or
        $installExecuteCompleted.Index -ge $installFinalizeStarted.Index -or $installFinalizeStarted.Index -ge $productUnregistered.Index) {
        throw 'Successful uninstall log does not prove PATH finalization in the first script and product-registration teardown in the final script.'
    }
}

function Get-TestFailureDiagnosticFiles([string[]]$ProductCodes) {
    if ([string]::IsNullOrWhiteSpace($TemporaryBase) -or -not (Test-Path -LiteralPath $TemporaryBase -PathType Container)) { return }
    foreach ($productCode in @($ProductCodes | Sort-Object -Unique)) {
        $stateLeaf = [System.IO.Path]::GetFileName((Join-Path $TemporaryBase "inlaid-path-$productCode.json"))
        $namePattern = '^' + [regex]::Escape($stateLeaf) + '\.(preflight|apply|uninstall|finalize|rollback|commit|fail)\.\d+\.test-error\.json$'
        foreach ($file in @(Get-ChildItem -LiteralPath $TemporaryBase -Filter ($stateLeaf + '.*.test-error.json') -File -ErrorAction Stop | Sort-Object Name)) {
            if ($file.Name -cmatch $namePattern) { Write-Output $file }
        }
    }
}

function Retain-LifecycleEvidence([string]$Outcome, [string]$Phase) {
    $run = $RetainedLifecycleEvidence
    New-Item -ItemType Directory -Force -Path $run | Out-Null
    $artifactRoot = Join-Path $run 'artifacts'
    New-Item -ItemType Directory -Force -Path $artifactRoot | Out-Null
    $candidates = @()
    function Get-EvidenceTreeCandidates([string]$Source, [string]$Destination) {
        if ([string]::IsNullOrWhiteSpace($Source) -or -not (Test-Path -LiteralPath $Source)) { return }
        if (Test-Path -LiteralPath $Source -PathType Leaf) {
            Write-Output ([pscustomobject]@{ source = $Source; destination = Join-Path $Destination (Split-Path -Leaf $Source) })
            return
        }
        foreach ($file in @(Get-ChildItem -LiteralPath $Source -File -Recurse -ErrorAction Stop | Sort-Object FullName)) {
            $relative = $file.FullName.Substring($Source.Length).TrimStart([char[]]@('\', '/'))
            Write-Output ([pscustomobject]@{ source = $file.FullName; destination = Join-Path $Destination $relative })
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($TemporaryRoot) -and (Test-Path -LiteralPath $TemporaryRoot -PathType Container)) {
        foreach ($log in @(Get-ChildItem -LiteralPath $TemporaryRoot -Filter '*.log' -File | Sort-Object Name)) {
            $candidates += [pscustomobject]@{ source = $log.FullName; destination = Join-Path $artifactRoot ('logs\' + $log.Name) }
        }
        $candidates += @(Get-EvidenceTreeCandidates (Join-Path $TemporaryRoot 'msi-clients') (Join-Path $artifactRoot 'msi-clients'))
    }
    $candidates += @(Get-EvidenceTreeCandidates $FirstEvidenceDirectory (Join-Path $artifactRoot 'first-evidence'))
    $candidates += @(Get-EvidenceTreeCandidates $SecondEvidenceDirectory (Join-Path $artifactRoot 'second-evidence'))
    $candidates += @(Get-EvidenceTreeCandidates $LifecycleSnapshotDirectory (Join-Path $artifactRoot 'snapshots'))
    foreach ($diagnostic in @(Get-TestFailureDiagnosticFiles $LifecycleProductCodes)) {
        $candidates += [pscustomobject]@{
            source = $diagnostic.FullName
            destination = Join-Path $artifactRoot ('helper-diagnostics\' + $diagnostic.Name)
            helperDiagnostic = $true
        }
    }
    foreach ($msi in @($First, $Second)) {
        if (-not [string]::IsNullOrWhiteSpace($msi)) {
            foreach ($artifact in @($msi, [System.IO.Path]::ChangeExtension($msi, '.wixpdb'), $msi + '.payload.json')) {
                if (Test-Path -LiteralPath $artifact -PathType Leaf) {
                    $candidates += [pscustomobject]@{ source = $artifact; destination = Join-Path $artifactRoot ('packages\' + (Split-Path -Leaf $artifact)) }
                }
            }
        }
    }
    $maximumFiles = 1024
    $maximumFileBytes = 128MB
    $maximumTotalBytes = 512MB
    [long]$copiedBytes = 0
    $copiedFiles = 0
    $omitted = @()
    foreach ($candidate in @($candidates | Sort-Object source -Unique)) {
        $file = Get-Item -LiteralPath $candidate.source -ErrorAction Stop
        if ($copiedFiles -ge $maximumFiles -or $file.Length -gt $maximumFileBytes -or ($copiedBytes + $file.Length) -gt $maximumTotalBytes) {
            $omitted += [ordered]@{ source = $candidate.source; length = $file.Length; reason = 'bounded evidence limit' }
            continue
        }
        $destinationParent = Split-Path -Parent $candidate.destination
        New-Item -ItemType Directory -Force -Path $destinationParent | Out-Null
        Copy-Item -LiteralPath $candidate.source -Destination $candidate.destination -Force
        if ($candidate.PSObject.Properties.Name -contains 'helperDiagnostic' -and $candidate.helperDiagnostic) {
            [void]$RetainedHelperDiagnosticPaths.Add([System.IO.Path]::GetFullPath($file.FullName))
        }
        $copiedFiles++
        $copiedBytes += $file.Length
    }
    $safePhase = $Phase -replace '[^A-Za-z0-9._-]', '-'
    [ordered]@{
        schema = 1; phase = $Phase; createdUtc = [DateTime]::UtcNow.ToString('o')
        copiedFiles = $copiedFiles; copiedBytes = $copiedBytes; omitted = $omitted
    } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $run ("retention-$safePhase.json")) -Encoding utf8NoBOM
    $inventory = @(Get-ChildItem -LiteralPath $run -File -Recurse | Where-Object {
        $_.Name -notin @('inventory.json', 'run.json')
    } | Sort-Object FullName | ForEach-Object {
        [ordered]@{
            path = $_.FullName.Substring($run.Length + 1)
            length = $_.Length
            sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
        }
    })
    ConvertTo-Json -InputObject @($inventory) -Depth 3 | Set-Content -LiteralPath (Join-Path $run 'inventory.json') -Encoding utf8NoBOM
    [ordered]@{
        schema = 2
        outcome = $Outcome
        phase = $Phase
        createdUtc = [DateTime]::UtcNow.ToString('o')
        temporaryRootRetained = (-not $LifecyclePassed)
        msiClientTimedOut = $MsiClientTimedOut
        windowsInstallerServiceSettled = if ($MsiClientTimedOut) { $false } else { $null }
        cleanupSuppressedReason = $CleanupSuppressedReason
        inventoryCount = $inventory.Count
    } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $run 'run.json') -Encoding utf8NoBOM
    if ($Outcome -ceq 'passed' -and $omitted.Count -ne 0) {
        throw "Passing MSI lifecycle evidence exceeded bounded retention limits; original working evidence is preserved. Omitted files: $($omitted.Count)"
    }
    return $run
}

function Get-ExpectedAppend([string]$Before) {
    if ($Before.Length -eq 0) { return $InstallRoot }
    return $Before + ';' + $InstallRoot
}

function Assert-TransactionSnapshotsAbsent([string[]]$ProductCodes, [string]$Context) {
    foreach ($productCode in $ProductCodes) {
        $state = Join-Path $TemporaryBase "inlaid-path-$productCode.json"
        foreach ($candidate in @($state, ($state + '.partial'), ($state + '.claim'), ($state + '.claim.partial'))) {
            if (Test-Path -LiteralPath $candidate) {
                throw "$Context found stale MSI PATH transaction state: $candidate"
            }
        }
    }
}

function Get-TransactionFilesSnapshot([string[]]$ProductCodes) {
    $rows = @()
    foreach ($productCode in @($ProductCodes | Sort-Object -Unique)) {
        $state = Join-Path $TemporaryBase "inlaid-path-$productCode.json"
        foreach ($candidate in @($state, ($state + '.partial'), ($state + '.claim'), ($state + '.claim.partial'))) {
            if (Test-Path -LiteralPath $candidate -PathType Leaf) {
                $file = Get-Item -LiteralPath $candidate
                $rows += [ordered]@{ path = $candidate; present = $true; length = $file.Length; sha256 = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash }
            }
            elseif (Test-Path -LiteralPath $candidate) {
                $rows += [ordered]@{ path = $candidate; present = $true; type = 'non-file' }
            }
            else { $rows += [ordered]@{ path = $candidate; present = $false } }
        }
    }
    return ($rows | ConvertTo-Json -Depth 3 -Compress)
}

function Get-NormalizedPathSegment([string]$Segment) {
    $value = $Segment.Trim()
    if ($value.Length -ge 2 -and $value[0] -eq '"' -and $value[$value.Length - 1] -eq '"') {
        $value = $value.Substring(1, $value.Length - 2)
    }
    $value = [Environment]::ExpandEnvironmentVariables($value)
    if ($value.Contains(';') -or -not [System.IO.Path]::IsPathFullyQualified($value)) { return $null }
    try { return [System.IO.Path]::GetFullPath($value).TrimEnd('\', '/') }
    catch { return $null }
}

function Assert-Marker([bool]$Owned, [string]$Inserted, [bool]$PathValueExistedBeforeOwnership) {
    if (-not (Test-Path -LiteralPath $MarkerRegistryPath)) { throw 'MSI PATH provenance marker is missing.' }
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($MarkerRegistrySubKey, $false)
    if ($null -eq $key) { throw 'MSI PATH provenance marker could not be opened.' }
    try {
        $expectedNames = $KnownMarkerValueNames
        $actualNames = @($key.GetValueNames())
        $missingNames = @($expectedNames | Where-Object { $actualNames -cnotcontains $_ })
        $unexpectedNames = @($actualNames | Where-Object { $expectedNames -cnotcontains $_ })
        if ($missingNames.Count -ne 0 -or $unexpectedNames.Count -ne 0) {
            throw "MSI PATH provenance marker value names mismatch. Missing='$($missingNames -join ',')' unexpected='$($unexpectedNames -join ',')'."
        }
        $subkeys = @($key.GetSubKeyNames())
        if ($subkeys.Count -ne 1 -or $subkeys[0] -cne 'Components') {
            throw "MSI PATH provenance marker subkeys mismatch: '$($subkeys -join ',')'."
        }

        foreach ($name in @('Schema', 'Owned', 'PathValueExistedBeforeOwnership', 'Component')) {
            if ($key.GetValueKind($name) -ne [Microsoft.Win32.RegistryValueKind]::DWord) {
                throw "MSI PATH provenance marker value '$name' is not REG_DWORD."
            }
        }
        foreach ($name in @('NormalizedProgramDirectory', 'InsertedSegment')) {
            if ($key.GetValueKind($name) -ne [Microsoft.Win32.RegistryValueKind]::String) {
                throw "MSI PATH provenance marker value '$name' is not REG_SZ."
            }
        }

        $expectedOwned = if ($Owned) { 1 } else { 0 }
        $expectedPriorPresence = if ($PathValueExistedBeforeOwnership) { 1 } else { 0 }
        if ([uint32]$key.GetValue('Schema') -ne 2 -or
            [uint32]$key.GetValue('Owned') -ne $expectedOwned -or
            [uint32]$key.GetValue('PathValueExistedBeforeOwnership') -ne $expectedPriorPresence -or
            [string]$key.GetValue('NormalizedProgramDirectory') -cne $InstallRoot -or
            [string]$key.GetValue('InsertedSegment') -cne $Inserted -or
            [uint32]$key.GetValue('Component') -ne 1) {
            throw "MSI PATH provenance marker mismatch: $(Get-MarkerSnapshot)"
        }

        $components = $key.OpenSubKey('Components', $false)
        if ($null -eq $components) { throw 'MSI component key-path registry key is missing.' }
        try {
            $componentNames = @($components.GetValueNames())
            $missingComponents = @($KnownComponentValueNames | Where-Object { $componentNames -cnotcontains $_ })
            $unexpectedComponents = @($componentNames | Where-Object { $KnownComponentValueNames -cnotcontains $_ })
            if ($missingComponents.Count -ne 0 -or $unexpectedComponents.Count -ne 0 -or $components.GetSubKeyNames().Count -ne 0) {
                throw "MSI component key-path structure mismatch. Missing='$($missingComponents -join ',')' unexpected='$($unexpectedComponents -join ',')'."
            }
            foreach ($name in $KnownComponentValueNames) {
                if ($components.GetValueKind($name) -ne [Microsoft.Win32.RegistryValueKind]::DWord -or
                    [uint32]$components.GetValue($name) -ne 1) {
                    throw "MSI component key-path value '$name' is not REG_DWORD 1."
                }
            }
        }
        finally { $components.Close() }
    }
    finally { $key.Close() }
}

function Get-MarkerSnapshot {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($MarkerRegistrySubKey, $false)
    if ($null -eq $key) { return '<absent>' }
    try {
        $snapshot = [ordered]@{}
        foreach ($name in @('Schema', 'NormalizedProgramDirectory', 'InsertedSegment', 'Owned', 'PathValueExistedBeforeOwnership', 'Component')) {
            if ($key.GetValueNames() -ccontains $name) {
                $snapshot[$name] = [ordered]@{
                    kind = [string]$key.GetValueKind($name)
                    value = $key.GetValue($name, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
                }
            }
            else { $snapshot[$name] = $null }
        }
        return ($snapshot | ConvertTo-Json -Depth 4 -Compress)
    }
    finally { $key.Close() }
}

function Get-InstallerRegistryStructureSnapshot {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($MarkerRegistrySubKey, $false)
    if ($null -eq $key) { return '<absent>' }
    try {
        $rootValues = @($key.GetValueNames() | Sort-Object | ForEach-Object {
            [ordered]@{
                name = $_
                kind = [string]$key.GetValueKind($_)
                raw = $key.GetValue($_, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
            }
        })
        $rootSubkeys = @($key.GetSubKeyNames() | Sort-Object)
        $components = $key.OpenSubKey('Components', $false)
        $componentEvidence = $null
        if ($null -ne $components) {
            try {
                $componentEvidence = [ordered]@{
                    values = @($components.GetValueNames() | Sort-Object | ForEach-Object {
                        [ordered]@{
                            name = $_
                            kind = [string]$components.GetValueKind($_)
                            raw = $components.GetValue($_, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
                        }
                    })
                    subkeys = @($components.GetSubKeyNames() | Sort-Object)
                }
            }
            finally { $components.Close() }
        }
        return ([ordered]@{ values = $rootValues; subkeys = $rootSubkeys; components = $componentEvidence } | ConvertTo-Json -Depth 6 -Compress)
    }
    finally { $key.Close() }
}

function Assert-InstallerRegistryStructureEmpty([string]$Context) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($MarkerRegistrySubKey, $false)
    if ($null -eq $key) { return }
    try {
        $rootValues = @($key.GetValueNames())
        $rootSubkeys = @($key.GetSubKeyNames())
        $unexpectedRootSubkeys = @($rootSubkeys | Where-Object { $_ -cne 'Components' })
        if ($rootValues.Count -ne 0 -or $unexpectedRootSubkeys.Count -ne 0 -or $rootSubkeys.Count -gt 1) {
            throw "$Context retained installer-owned or foreign root registry state: $(Get-InstallerRegistryStructureSnapshot)"
        }
        if ($rootSubkeys.Count -eq 1) {
            $components = $key.OpenSubKey('Components', $false)
            if ($null -eq $components) { throw "$Context could not open the enumerated Components key." }
            try {
                if ($components.GetValueNames().Count -ne 0 -or $components.GetSubKeyNames().Count -ne 0) {
                    throw "$Context retained installer-owned or foreign Components registry state: $(Get-InstallerRegistryStructureSnapshot)"
                }
            }
            finally { $components.Close() }
        }
    }
    finally { $key.Close() }
}

function ConvertTo-PackedProductCode([string]$ProductCode) {
    $parts = $ProductCode.Trim('{}').ToUpperInvariant().Split('-')
    if ($parts.Count -ne 5) { throw "Invalid ProductCode: $ProductCode" }
    function Reverse-Text([string]$Value) {
        $characters = $Value.ToCharArray()
        [Array]::Reverse($characters)
        return -join $characters
    }
    function Swap-Pairs([string]$Value) {
        if (($Value.Length % 2) -ne 0) { throw "Invalid ProductCode segment: $Value" }
        $result = ''
        for ($index = 0; $index -lt $Value.Length; $index += 2) {
            $result += $Value[$index + 1]
            $result += $Value[$index]
        }
        return $result
    }
    return (Reverse-Text $parts[0]) + (Reverse-Text $parts[1]) + (Reverse-Text $parts[2]) +
        (Swap-Pairs $parts[3]) + (Swap-Pairs $parts[4])
}

function Get-InlaidRegistrationEvidence {
    $arp = @()
    foreach ($root in @('HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
                        'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
                        'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall')) {
        if (Test-Path -LiteralPath $root) {
            $arp += @(Get-ChildItem -LiteralPath $root -ErrorAction SilentlyContinue |
                Where-Object { $_.GetValue('DisplayName') -eq 'Inlaid' })
        }
    }
    $packedProducts = @($LifecycleProductCodes | ForEach-Object { ConvertTo-PackedProductCode $_ } | Sort-Object -Unique)
    $hkcuInstaller = @()
    $hkcuRoot = 'HKCU:\Software\Microsoft\Installer\Products'
    if (Test-Path -LiteralPath $hkcuRoot) {
        $hkcuInstaller = @(Get-ChildItem -LiteralPath $hkcuRoot -ErrorAction SilentlyContinue |
            Where-Object { $_.GetValue('ProductName') -eq 'Inlaid' -or $packedProducts -ccontains $_.PSChildName })
        $packedProducts = @($packedProducts + @($hkcuInstaller.PSChildName) | Sort-Object -Unique)
    }
    $hkcuFeatures = @()
    $hkcuFeaturesRoot = 'HKCU:\Software\Microsoft\Installer\Features'
    if (Test-Path -LiteralPath $hkcuFeaturesRoot) {
        $hkcuFeatures = @(Get-ChildItem -LiteralPath $hkcuFeaturesRoot -ErrorAction SilentlyContinue |
            Where-Object { $packedProducts -ccontains $_.PSChildName })
    }
    $packedUpgrade = ConvertTo-PackedProductCode $UpgradeCode
    $hkcuUpgradeCodes = @()
    $hkcuUpgradeRoot = 'HKCU:\Software\Microsoft\Installer\UpgradeCodes'
    if (Test-Path -LiteralPath $hkcuUpgradeRoot) {
        $hkcuUpgradeCodes = @(Get-ChildItem -LiteralPath $hkcuUpgradeRoot -ErrorAction SilentlyContinue |
            Where-Object { $_.PSChildName -ceq $packedUpgrade -or @($_.GetValueNames() | Where-Object { $packedProducts -ccontains $_ }).Count -gt 0 })
        foreach ($key in $hkcuUpgradeCodes) {
            $packedProducts = @($packedProducts + @($key.GetValueNames()) | Sort-Object -Unique)
        }
        foreach ($packedProduct in $packedProducts) {
            foreach ($pair in @(
                [pscustomobject]@{ Root = $hkcuRoot; Kind = 'product' },
                [pscustomobject]@{ Root = $hkcuFeaturesRoot; Kind = 'feature' }
            )) {
                $candidate = Join-Path $pair.Root $packedProduct
                if (Test-Path -LiteralPath $candidate) {
                    if ($pair.Kind -ceq 'product') { $hkcuInstaller += Get-Item -LiteralPath $candidate }
                    else { $hkcuFeatures += Get-Item -LiteralPath $candidate }
                }
            }
        }
        $hkcuInstaller = @($hkcuInstaller | Sort-Object Name -Unique)
        $hkcuFeatures = @($hkcuFeatures | Sort-Object Name -Unique)
    }
    $userData = @()
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $userDataRoot = "HKLM:\Software\Microsoft\Windows\CurrentVersion\Installer\UserData\$sid\Products"
    if (Test-Path -LiteralPath $userDataRoot) {
        $userData = @(Get-ChildItem -LiteralPath $userDataRoot -ErrorAction SilentlyContinue | Where-Object {
            $properties = Join-Path $_.PSPath 'InstallProperties'
            ($packedProducts -ccontains $_.PSChildName) -or ((Test-Path -LiteralPath $properties) -and (Get-ItemProperty -LiteralPath $properties).DisplayName -eq 'Inlaid')
        })
        $packedProducts = @($packedProducts + @($userData.PSChildName) | Sort-Object -Unique)
    }
    $userDataComponents = @()
    $userDataComponentsRoot = "HKLM:\Software\Microsoft\Windows\CurrentVersion\Installer\UserData\$sid\Components"
    if (Test-Path -LiteralPath $userDataComponentsRoot) {
        $userDataComponents = @(Get-ChildItem -LiteralPath $userDataComponentsRoot -ErrorAction SilentlyContinue |
            Where-Object { @($_.GetValueNames() | Where-Object { $packedProducts -ccontains $_ }).Count -gt 0 })
    }
    $all = @($arp) + @($hkcuInstaller) + @($hkcuFeatures) + @($hkcuUpgradeCodes) + @($userData) + @($userDataComponents)
    $all = @($all | Sort-Object Name -Unique)
    return [pscustomobject]@{
        Arp = @($arp); HkcuInstaller = @($hkcuInstaller); HkcuFeatures = @($hkcuFeatures)
        HkcuUpgradeCodes = @($hkcuUpgradeCodes); UserData = @($userData)
        UserDataComponents = @($userDataComponents); All = $all
    }
}

function Get-InlaidRegistrationSnapshot {
    $evidence = Get-InlaidRegistrationEvidence
    $roots = @($evidence.All)
    $keys = @()
    foreach ($root in @($roots | Sort-Object Name)) {
        $tree = @($root) + @(Get-ChildItem -LiteralPath $root.PSPath -Recurse -ErrorAction Stop)
        foreach ($key in @($tree | Sort-Object Name)) {
            $values = @()
            foreach ($name in @($key.GetValueNames() | Sort-Object)) {
                $kind = $key.GetValueKind($name)
                $raw = $key.GetValue($name, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
                $data = switch ($kind) {
                    ([Microsoft.Win32.RegistryValueKind]::Binary) { [Convert]::ToBase64String([byte[]]$raw); break }
                    ([Microsoft.Win32.RegistryValueKind]::None) { [Convert]::ToBase64String([byte[]]$raw); break }
                    ([Microsoft.Win32.RegistryValueKind]::MultiString) { @([string[]]$raw); break }
                    ([Microsoft.Win32.RegistryValueKind]::DWord) { ([uint32]$raw).ToString([Globalization.CultureInfo]::InvariantCulture); break }
                    ([Microsoft.Win32.RegistryValueKind]::QWord) { ([uint64]$raw).ToString([Globalization.CultureInfo]::InvariantCulture); break }
                    default { [string]$raw }
                }
                $values += [ordered]@{ name = $name; kind = [string]$kind; raw = $data }
            }
            $keys += [ordered]@{ path = $key.Name; values = $values }
        }
    }
    $cachedPackages = @($evidence.UserData | Sort-Object Name | ForEach-Object {
        $properties = Get-Item -LiteralPath (Join-Path $_.PSPath 'InstallProperties')
        $localPackage = [string]$properties.GetValue('LocalPackage', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if (-not (Test-Path -LiteralPath $localPackage -PathType Leaf)) {
            throw "Registered cached MSI is missing: $localPackage"
        }
        $file = Get-Item -LiteralPath $localPackage
        [ordered]@{
            path = $file.FullName
            length = $file.Length
            sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash
        }
    })
    return ([ordered]@{ keys = $keys; cachedPackages = $cachedPackages; volatileExclusions = @() } | ConvertTo-Json -Depth 12 -Compress)
}

function Assert-NoInlaidRegistration {
    $evidence = Get-InlaidRegistrationEvidence
    if ($evidence.All.Count -ne 0) {
        throw 'Refusing lifecycle work while any Inlaid Windows Installer product context remains.'
    }
}

function Assert-Registration([string]$ExpectedVersion, [string]$ExpectedProductCode) {
    $installer = New-Object -ComObject WindowsInstaller.Installer
    try {
        $state = [int]$installer.ProductState($ExpectedProductCode)
        $name = [string]$installer.ProductInfo($ExpectedProductCode, 'ProductName')
        $version = [string]$installer.ProductInfo($ExpectedProductCode, 'VersionString')
        $assignment = [int]$installer.ProductInfo($ExpectedProductCode, 'AssignmentType')
        $localPackage = [string]$installer.ProductInfo($ExpectedProductCode, 'LocalPackage')
    }
    catch { throw "Windows Installer did not resolve expected product $ExpectedProductCode." }
    finally {
        if ([Runtime.InteropServices.Marshal]::IsComObject($installer)) {
            [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($installer)
        }
    }
    if ($state -ne 5 -or $name -cne 'Inlaid' -or $version -cne $ExpectedVersion -or $assignment -ne 0 -or
        -not (Test-Path -LiteralPath $localPackage -PathType Leaf)) {
        throw "Windows Installer product identity/context mismatch for $ExpectedProductCode."
    }

    $packed = ConvertTo-PackedProductCode $ExpectedProductCode
    $evidence = Get-InlaidRegistrationEvidence
    $arp = @($evidence.Arp | Where-Object { $_.PSChildName -ceq $ExpectedProductCode })
    $hkcu = @($evidence.HkcuInstaller | Where-Object { $_.PSChildName -ceq $packed })
    $features = @($evidence.HkcuFeatures | Where-Object { $_.PSChildName -ceq $packed })
    $userData = @($evidence.UserData | Where-Object { $_.PSChildName -ceq $packed })
    $expectedComponentKeys = @($ComponentCodes | ForEach-Object { ConvertTo-PackedProductCode $_ })
    $actualComponentKeys = @($evidence.UserDataComponents | ForEach-Object PSChildName)
    if ($evidence.Arp.Count -ne 1 -or $arp.Count -ne 1 -or
        [string]$arp[0].GetValue('DisplayVersion') -cne $ExpectedVersion -or
        $evidence.HkcuInstaller.Count -ne 1 -or $hkcu.Count -ne 1 -or
        $evidence.HkcuFeatures.Count -ne 1 -or $features.Count -ne 1 -or
        $evidence.HkcuUpgradeCodes.Count -ne 1 -or
        $evidence.UserData.Count -ne 1 -or $userData.Count -ne 1 -or
        $evidence.UserDataComponents.Count -ne $ComponentCodes.Count -or
        (Compare-Object -ReferenceObject $expectedComponentKeys -DifferenceObject $actualComponentKeys)) {
        throw "Windows Installer registration context mismatch for $ExpectedProductCode."
    }
}

function Assert-ProductAbsent([string]$ProductCode) {
    $installer = New-Object -ComObject WindowsInstaller.Installer
    try { $state = [int]$installer.ProductState($ProductCode) }
    catch { $state = -1 }
    finally {
        if ([Runtime.InteropServices.Marshal]::IsComObject($installer)) {
            [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($installer)
        }
    }
    if ($state -ne -1) { throw "Unexpected Windows Installer product remains registered: $ProductCode state=$state" }
}

function Assert-InstalledPayload([string]$Version, [string]$MsiVersion, [string]$ProductCode, [object]$PackageEvidence) {
    if ([string]$PackageEvidence.identity.productCode -cne $ProductCode -or
        [string]$PackageEvidence.identity.productVersion -cne $MsiVersion) {
        throw 'Installed-payload assertion received package evidence for a different MSI identity.'
    }
    $expectedRows = @($PackageEvidence.payload)
    $Expected = @($expectedRows | ForEach-Object { ([string]$_.destination).Replace('/', '\') } | Sort-Object)
    $Actual = @(Get-ChildItem -LiteralPath $InstallRoot -File -Recurse | ForEach-Object { $_.FullName.Substring($InstallRoot.Length + 1) } | Sort-Object)
    if (Compare-Object -ReferenceObject $Expected -DifferenceObject $Actual) { throw 'Installed MSI payload does not exactly match the Windows MSI profile.' }
    foreach ($expected in $expectedRows) {
        $destination = ([string]$expected.destination).Replace('/', '\')
        $installed = Join-Path $InstallRoot $destination
        $file = Get-Item -LiteralPath $installed
        $hash = (Get-FileHash -LiteralPath $installed -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($file.Length -ne [long]$expected.length -or $hash -cne ([string]$expected.exportedSHA256).ToLowerInvariant()) {
            throw "Installed MSI payload bytes do not match decompiled package evidence: $destination"
        }
    }
    if (Test-Path -LiteralPath $StartMenu) { throw 'Terminal-first MSI created a Start menu shortcut.' }
    foreach ($launcher in @('START-INLAID.cmd', 'START-INLAID.ps1')) {
        if (Test-Path -LiteralPath (Join-Path $InstallRoot $launcher)) { throw "Installed payload contains source launcher $launcher." }
    }
    $versionText = @(& (Join-Path $InstallRoot 'inlaid.exe') --version)
    if ($LASTEXITCODE -ne 0 -or $versionText.Count -ne 1 -or $versionText[0] -cne "Inlaid $Version") { throw 'Installed executable identity mismatch.' }
    $previewOne = @(& (Join-Path $InstallRoot 'inlaid.exe') --render-preview 80x24)
    $previewTwo = @(& (Join-Path $InstallRoot 'inlaid.exe') --render-preview 80x24)
    if (($previewOne -join "`n") -cne ($previewTwo -join "`n") -or ($previewOne -join "`n") -notmatch 'INLAID') {
        throw 'Installed deterministic preview mismatch.'
    }
    Assert-Registration $MsiVersion $ProductCode
}

function Get-UserDataSnapshot {
    $rows = @($UserData | Sort-Object | ForEach-Object {
        if (-not (Test-Path -LiteralPath $_ -PathType Leaf)) { throw "MSI lifecycle user-data fixture is missing: $_" }
        $file = Get-Item -LiteralPath $_
        [ordered]@{ path = $file.FullName; length = $file.Length; sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash }
    })
    return ($rows | ConvertTo-Json -Depth 3 -Compress)
}

function Get-ExistingUserDataEvidence {
    return @($UserData | Sort-Object | ForEach-Object {
        if (Test-Path -LiteralPath $_ -PathType Leaf) {
            $file = Get-Item -LiteralPath $_
            [ordered]@{ path = $file.FullName; length = $file.Length; sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash }
        }
        else { [ordered]@{ path = $_; absent = $true } }
    })
}

function Save-LifecycleSnapshot([string]$Name) {
    if (-not (Test-Path -LiteralPath $LifecycleSnapshotDirectory -PathType Container)) {
        New-Item -ItemType Directory -Path $LifecycleSnapshotDirectory | Out-Null
    }
    $registration = Get-InlaidRegistrationSnapshot | ConvertFrom-Json
    $pathState = Get-ExactUserPathState
    $markerText = Get-MarkerSnapshot
    $snapshot = [ordered]@{
        schema = 1; name = $Name; createdUtc = [DateTime]::UtcNow.ToString('o')
        path = [ordered]@{
            present = $pathState.Present
            raw = $pathState.Raw
            kind = if ($null -eq $pathState.Kind) { $null } else { [string]$pathState.Kind }
            type = $pathState.Type
            rawHex = $pathState.RawHex
        }
        provenance = if ($markerText -ceq '<absent>') { $null } else { $markerText | ConvertFrom-Json }
        installerStructure = $(
            $structure = Get-InstallerRegistryStructureSnapshot
            if ($structure -ceq '<absent>') { $null } else { $structure | ConvertFrom-Json }
        )
        installTree = Get-InstallTreeSnapshot
        registration = $registration
        transactionFiles = (Get-TransactionFilesSnapshot $LifecycleProductCodes | ConvertFrom-Json)
        userData = Get-ExistingUserDataEvidence
    }
    $safeName = $Name -replace '[^A-Za-z0-9._-]', '-'
    $snapshot | ConvertTo-Json -Depth 16 | Set-Content -LiteralPath (Join-Path $LifecycleSnapshotDirectory ($safeName + '.json')) -Encoding utf8NoBOM
}

function Assert-UserData {
    if ([string]::IsNullOrWhiteSpace($ExpectedUserDataSnapshot)) { throw 'Expected user-data hash inventory was not captured.' }
    $actual = Get-UserDataSnapshot
    if ($actual -cne $ExpectedUserDataSnapshot) { throw 'MSI lifecycle changed the exact path, length, or SHA-256 of retained user data.' }
}

    $InitializationPhase = 'lifecycle preflight'
    if (-not $TemporaryRoot.StartsWith($TemporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) { throw 'MSI test directory escaped the system temporary directory.' }
    if (Test-Path -LiteralPath $InstallRoot) { throw "Refusing to test over an existing install: $InstallRoot" }
    Assert-InstallerRegistryStructureEmpty 'Lifecycle preflight'
    if (Test-Path -LiteralPath $StartMenu) { throw "Refusing to overwrite an existing Inlaid shortcut: $StartMenu" }
    Assert-NoInlaidRegistration
    foreach ($path in $UserData) { if (Test-Path -LiteralPath $path) { throw "Refusing to overwrite existing user data: $path" } }
    $equivalent = @($OriginalPath -split ';' | ForEach-Object { Get-NormalizedPathSegment $_ } | Where-Object { $null -ne $_ -and $_ -ieq $InstallRoot })
    if ($equivalent.Count -ne 0) { throw 'Refusing to test while the user PATH already contains an equivalent Inlaid install directory.' }

    New-Item -ItemType Directory -Path $TemporaryRoot | Out-Null
    $FirstVersion = 'v0.0.1-phase3'
    $SecondVersion = 'v0.0.2-phase3'
    $FirstMsiVersion = '0.0.1'
    $SecondMsiVersion = '0.0.2'
    $FirstExecutable = Join-Path $TemporaryRoot 'inlaid-first.exe'
    $SecondExecutable = Join-Path $TemporaryRoot 'inlaid-second.exe'
    Push-Location -LiteralPath $ProjectRoot
    try {
        $savedGoos, $savedGoarch, $savedCgo = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
        $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
        & go build -trimpath -ldflags "-X main.version=$FirstVersion" -o $FirstExecutable '.\cmd\inlaid'
        if ($LASTEXITCODE -ne 0) { throw 'First MSI fixture executable build failed.' }
        & go build -trimpath -ldflags "-X main.version=$SecondVersion" -o $SecondExecutable '.\cmd\inlaid'
        if ($LASTEXITCODE -ne 0) { throw 'Second MSI fixture executable build failed.' }
    }
    finally {
        $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $savedGoos, $savedGoarch, $savedCgo
        Pop-Location
    }
    $FirstDirectory = Join-Path $TemporaryRoot 'first'
    $SecondDirectory = Join-Path $TemporaryRoot 'second'
    $First = Join-Path $FirstDirectory "inlaid-$FirstVersion-windows-x64.msi"
    $Second = Join-Path $SecondDirectory "inlaid-$SecondVersion-windows-x64.msi"
    & $BuildScript -Version $FirstVersion -MsiVersion $FirstMsiVersion -Executable $FirstExecutable -OutputDirectory $FirstDirectory -Wix $Wix -TestBuild -EnableTestHooks
    & $BuildScript -Version $SecondVersion -MsiVersion $SecondMsiVersion -Executable $SecondExecutable -OutputDirectory $SecondDirectory -Wix $Wix -TestBuild -EnableTestHooks
    $FirstEvidenceDirectory = Join-Path $TemporaryRoot 'first-evidence'
    $SecondEvidenceDirectory = Join-Path $TemporaryRoot 'second-evidence'
    & $InspectScript -Msi $First -Wix $Wix -ExpectedVersion $FirstMsiVersion -Executable $FirstExecutable -EvidenceDirectory $FirstEvidenceDirectory -ExpectedPathHelperTestHooks $true
    & $InspectScript -Msi $Second -Wix $Wix -ExpectedVersion $SecondMsiVersion -Executable $SecondExecutable -EvidenceDirectory $SecondEvidenceDirectory -ExpectedPathHelperTestHooks $true
    $FirstEvidence = Get-Content -LiteralPath (Join-Path $FirstEvidenceDirectory 'msi-evidence.json') -Raw | ConvertFrom-Json
    $SecondEvidence = Get-Content -LiteralPath (Join-Path $SecondEvidenceDirectory 'msi-evidence.json') -Raw | ConvertFrom-Json
    $FirstProductCode = [string]$FirstEvidence.identity.productCode
    $SecondProductCode = [string]$SecondEvidence.identity.productCode
    if ($FirstProductCode -ceq $SecondProductCode -or
        [string]$FirstEvidence.identity.packageCode -ceq [string]$SecondEvidence.identity.packageCode -or
        [string]$FirstEvidence.identity.upgradeCode -cne [string]$SecondEvidence.identity.upgradeCode) {
        throw 'MSI fixtures do not use changing product/package identity under one stable UpgradeCode.'
    }
    $LifecycleProductCodes = @($FirstProductCode, $SecondProductCode)
    Assert-NoInlaidRegistration
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Lifecycle preflight'
    Save-LifecycleSnapshot '00-preflight'
    $LifecycleMutationStarted = $true
    $InitializationPhase = 'lifecycle execution'
    Assert-FailedMsi @('/i', $First, '/qn', 'DISABLEROLLBACK=1') 'Rollback-disabled launch condition' 'rollback-disabled.log'
    Assert-RollbackDisabledLog 'rollback-disabled.log'
    Assert-NoInlaidRegistration
    Assert-OriginalUserPath 'Rollback-disabled launch rejection'
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Rollback-disabled launch rejection'
    if (Test-Path -LiteralPath $InstallRoot) { throw 'Rollback-disabled launch rejection changed payload state.' }
    Assert-InstallerRegistryStructureEmpty 'Rollback-disabled launch rejection'
    Save-LifecycleSnapshot '01-rollback-disabled'

    $LegalRawPathCases = @(
        [pscustomobject]@{ Name = 'reg-sz-zero-byte'; Type = [uint32]1; Decoded = ''; Bytes = [byte[]]@() },
        [pscustomobject]@{ Name = 'reg-expand-sz-unterminated'; Type = [uint32]2; Decoded = 'C:\InlaidRawUnterminated'; Bytes = [Text.Encoding]::Unicode.GetBytes('C:\InlaidRawUnterminated') },
        [pscustomobject]@{ Name = 'reg-sz-single-nul'; Type = [uint32]1; Decoded = 'C:\InlaidRawSingleNul'; Bytes = ConvertTo-RegistryStringBytes 'C:\InlaidRawSingleNul' },
        [pscustomobject]@{ Name = 'reg-expand-sz-multi-trailing-nul'; Type = [uint32]2; Decoded = 'C:\InlaidRawMultiNul'; Bytes = [byte[]](@([Text.Encoding]::Unicode.GetBytes('C:\InlaidRawMultiNul')) + @(0, 0, 0, 0, 0, 0)) }
    )
    $RawCaseIndex = 0
    foreach ($rawCase in $LegalRawPathCases) {
        $RawCaseIndex++
        Set-RawUserPath $rawCase.Type $rawCase.Bytes
        Assert-RawUserPathState $true $rawCase.Type $rawCase.Bytes "$($rawCase.Name) setup"
        Assert-NoInlaidRegistration
        Assert-InstallerRegistryStructureEmpty "$($rawCase.Name) setup"
        Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context "$($rawCase.Name) setup"

        $failedInstallLog = "raw-$($rawCase.Name)-failed-install.log"
        Assert-FailedMsi @('/i', $First, '/qn', 'INLAID_TEST_INJECT_FAILURE=1') "Injected failed install for $($rawCase.Name)" $failedInstallLog
        Assert-ActionFailureLog $failedInstallLog 'FailAfterUserPath'
        Assert-RawUserPathState $true $rawCase.Type $rawCase.Bytes "$($rawCase.Name) failed-install rollback"
        Assert-NoInlaidRegistration
        if (Test-Path -LiteralPath $InstallRoot) { throw "$($rawCase.Name) failed install retained package payload." }
        Assert-InstallerRegistryStructureEmpty "$($rawCase.Name) failed-install rollback"
        Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context "$($rawCase.Name) failed-install rollback"
        Save-LifecycleSnapshot ('02-raw-{0:D2}-{1}-failed-install' -f $RawCaseIndex, $rawCase.Name)

        $installLog = "raw-$($rawCase.Name)-install.log"
        Invoke-Msi @('/i', $First, '/qn') "Install for $($rawCase.Name) failed-uninstall evidence" $installLog
        $InstalledMsi = $First
        Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
        Assert-Marker $true $InstallRoot $true
        $expectedInstalledRaw = Get-ExpectedAppend $rawCase.Decoded
        $expectedInstalledBytes = ConvertTo-RegistryStringBytes $expectedInstalledRaw
        Assert-RawUserPathState $true $rawCase.Type $expectedInstalledBytes "$($rawCase.Name) successful install canonicalization"
        Assert-ExactUserPathState $true $expectedInstalledRaw ([Microsoft.Win32.RegistryValueKind]$rawCase.Type) "$($rawCase.Name) successful install semantics"
        $installedRawState = Get-ExactUserPathSnapshot
        $installedStructure = Get-InstallerRegistryStructureSnapshot
        $installedRegistration = Get-InlaidRegistrationSnapshot

        $failedUninstallLog = "raw-$($rawCase.Name)-failed-uninstall.log"
        Assert-FailedMsi @('/x', $First, '/qn', 'INLAID_TEST_FAIL_AFTER_FINALIZE_USER_PATH_MARKER=1') "Injected failed uninstall for $($rawCase.Name)" $failedUninstallLog
        Assert-PostFinalizeUserPathMarkerFailureLog $failedUninstallLog
        Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
        Assert-Marker $true $InstallRoot $true
        if ((Get-ExactUserPathSnapshot) -cne $installedRawState -or
            (Get-InstallerRegistryStructureSnapshot) -cne $installedStructure -or
            (Get-InlaidRegistrationSnapshot) -cne $installedRegistration) {
            throw "$($rawCase.Name) failed uninstall did not restore installed PATH bytes, registry structure, and registration exactly."
        }
        Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context "$($rawCase.Name) failed-uninstall rollback"
        Save-LifecycleSnapshot ('02-raw-{0:D2}-{1}-failed-uninstall' -f $RawCaseIndex, $rawCase.Name)

        $uninstallLog = "raw-$($rawCase.Name)-uninstall.log"
        Invoke-Msi @('/x', $First, '/qn') "Uninstall for $($rawCase.Name)" $uninstallLog
        Assert-ActionSuccessLog $uninstallLog 'FinalizeUserPathMarker'
        Assert-SplitUninstallRegistrationLog $uninstallLog
        $InstalledMsi = ''
        Assert-NoInlaidRegistration
        if (Test-Path -LiteralPath $InstallRoot) { throw "$($rawCase.Name) uninstall retained package payload." }
        $canonicalUninstallBytes = ConvertTo-RegistryStringBytes $rawCase.Decoded
        Assert-RawUserPathState $true $rawCase.Type $canonicalUninstallBytes "$($rawCase.Name) uninstall canonicalization"
        $expectedKind = [Microsoft.Win32.RegistryValueKind]$rawCase.Type
        Assert-ExactUserPathState $true $rawCase.Decoded $expectedKind "$($rawCase.Name) uninstall semantic restoration"
        Assert-InstallerRegistryStructureEmpty "$($rawCase.Name) uninstall"
        Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context "$($rawCase.Name) uninstall"
        Save-LifecycleSnapshot ('02-raw-{0:D2}-{1}' -f $RawCaseIndex, $rawCase.Name)
    }

    $MalformedRawPathCases = @(
        [pscustomobject]@{ Name = 'odd-byte-count'; Type = [uint32]1; Bytes = [byte[]]@(0x41) },
        [pscustomobject]@{ Name = 'embedded-nul-followed-by-content'; Type = [uint32]2; Bytes = [byte[]]@(0x41, 0, 0, 0, 0x42, 0, 0, 0) },
        [pscustomobject]@{ Name = 'unpaired-surrogate'; Type = [uint32]1; Bytes = [byte[]]@(0, 0xD8, 0, 0) }
    )
    foreach ($rawCase in $MalformedRawPathCases) {
        Set-RawUserPath $rawCase.Type $rawCase.Bytes
        Assert-RawUserPathState $true $rawCase.Type $rawCase.Bytes "$($rawCase.Name) setup"
        Assert-NoInlaidRegistration
        if (Test-Path -LiteralPath $InstallRoot) { throw "$($rawCase.Name) setup found package payload." }
        Assert-InstallerRegistryStructureEmpty "$($rawCase.Name) setup"
        Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context "$($rawCase.Name) setup"
        $beforeStructure = Get-InstallerRegistryStructureSnapshot
        $beforeTransactions = Get-TransactionFilesSnapshot $LifecycleProductCodes
        $failureLog = "raw-malformed-$($rawCase.Name).log"
        Assert-FailedMsi @('/i', $First, '/qn') "Malformed raw PATH rejection for $($rawCase.Name)" $failureLog
        Assert-ActionFailureLog $failureLog 'ApplyUserPath'
        Assert-RawUserPathState $true $rawCase.Type $rawCase.Bytes "$($rawCase.Name) rejection"
        Assert-NoInlaidRegistration
        if (Test-Path -LiteralPath $InstallRoot) { throw "$($rawCase.Name) rejection retained package payload." }
        if ((Get-InstallerRegistryStructureSnapshot) -cne $beforeStructure -or
            (Get-TransactionFilesSnapshot $LifecycleProductCodes) -cne $beforeTransactions) {
            throw "$($rawCase.Name) rejection changed marker or transaction state."
        }
        Assert-InstallerRegistryStructureEmpty "$($rawCase.Name) rejection"
        Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context "$($rawCase.Name) rejection"
        Save-LifecycleSnapshot "02-raw-malformed-$($rawCase.Name)"
    }
    Restore-OriginalUserPath
    Assert-OriginalUserPath 'Original PATH restore after raw-byte cases'

    [InlaidNativeUserPath]::Delete()
    Assert-ExactUserPathState $false '' $null 'Absent-PATH setup'
    Save-LifecycleSnapshot '02-absent-path-before-install'
    Assert-FailedMsi @('/i', $First, '/qn', 'INLAID_TEST_INJECT_FAILURE=1') 'Injected failed install from absent PATH' 'absent-path-failed-install.log'
    Assert-ActionFailureLog 'absent-path-failed-install.log' 'FailAfterUserPath'
    Assert-NoInlaidRegistration
    Assert-ExactUserPathState $false '' $null 'Absent-PATH failed-install rollback'
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Absent-PATH failed-install rollback'
    if (Test-Path -LiteralPath $InstallRoot) { throw 'Failed install from absent PATH retained payload.' }
    Assert-InstallerRegistryStructureEmpty 'Failed install from absent PATH'
    Save-LifecycleSnapshot '03-absent-path-failed-install'
    Invoke-Msi @('/i', $First, '/qn') 'Absent-PATH clean install' 'absent-path-install.log'
    $InstalledMsi = $First
    Assert-ExactUserPathState $true $InstallRoot ([Microsoft.Win32.RegistryValueKind]::ExpandString) 'Absent-PATH clean install'
    Assert-Marker $true $InstallRoot $false
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Save-LifecycleSnapshot '04-absent-path-install'
    $BeforeAbsentPathFailedUninstall = [ordered]@{
        path = Get-ExactUserPathSnapshot
        marker = Get-MarkerSnapshot
        installerStructure = Get-InstallerRegistryStructureSnapshot
        installTree = Get-InstallTreeSnapshot
        registration = Get-InlaidRegistrationSnapshot
        transactionFiles = Get-TransactionFilesSnapshot $LifecycleProductCodes
    }
    Assert-FailedMsi @('/x', $First, '/qn', 'INLAID_TEST_FAIL_AFTER_FINALIZE_USER_PATH_MARKER=1') 'Injected failed uninstall from originally absent PATH' 'absent-path-failed-uninstall.log'
    Assert-PostFinalizeUserPathMarkerFailureLog 'absent-path-failed-uninstall.log'
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Assert-ProductAbsent $SecondProductCode
    Assert-ExactUserPathState $true $InstallRoot ([Microsoft.Win32.RegistryValueKind]::ExpandString) 'Absent-PATH failed-uninstall rollback'
    Assert-Marker $true $InstallRoot $false
    if ((Get-ExactUserPathSnapshot) -cne $BeforeAbsentPathFailedUninstall.path -or
        (Get-MarkerSnapshot) -cne $BeforeAbsentPathFailedUninstall.marker -or
        (Get-InstallerRegistryStructureSnapshot) -cne $BeforeAbsentPathFailedUninstall.installerStructure -or
        (Get-InstallTreeSnapshot) -cne $BeforeAbsentPathFailedUninstall.installTree -or
        (Get-InlaidRegistrationSnapshot) -cne $BeforeAbsentPathFailedUninstall.registration -or
        (Get-TransactionFilesSnapshot $LifecycleProductCodes) -cne $BeforeAbsentPathFailedUninstall.transactionFiles) {
        throw 'Failed uninstall from originally absent PATH did not restore product registration, payload, marker, PATH, and transaction files exactly.'
    }
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Absent-PATH failed-uninstall rollback'
    Save-LifecycleSnapshot '05-absent-path-failed-uninstall'
    Invoke-Msi @('/x', $First, '/qn') 'Absent-PATH uninstall' 'absent-path-uninstall.log'
    Assert-ActionSuccessLog 'absent-path-uninstall.log' 'FinalizeUserPathMarker'
    Assert-SplitUninstallRegistrationLog 'absent-path-uninstall.log'
    $InstalledMsi = ''
    Assert-NoInlaidRegistration
    Assert-ExactUserPathState $false '' $null 'Absent-PATH uninstall restore'
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Absent-PATH uninstall restore'
    Assert-InstallerRegistryStructureEmpty 'Absent-PATH uninstall'
    Save-LifecycleSnapshot '06-absent-path-uninstall'

    Set-RawUserPath 2 (ConvertTo-RegistryStringBytes '')
    Assert-ExactUserPathState $true '' ([Microsoft.Win32.RegistryValueKind]::ExpandString) 'Present-empty-PATH setup'
    Save-LifecycleSnapshot '07-present-empty-path-before-install'
    Invoke-Msi @('/i', $First, '/qn') 'Present-empty-PATH clean install' 'present-empty-path-install.log'
    $InstalledMsi = $First
    Assert-ExactUserPathState $true $InstallRoot ([Microsoft.Win32.RegistryValueKind]::ExpandString) 'Present-empty-PATH clean install'
    Assert-Marker $true $InstallRoot $true
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Save-LifecycleSnapshot '08-present-empty-path-install'
    $BeforePresentEmptyPathFailedUninstall = [ordered]@{
        path = Get-ExactUserPathSnapshot
        marker = Get-MarkerSnapshot
        installerStructure = Get-InstallerRegistryStructureSnapshot
        installTree = Get-InstallTreeSnapshot
        registration = Get-InlaidRegistrationSnapshot
        transactionFiles = Get-TransactionFilesSnapshot $LifecycleProductCodes
    }
    Assert-FailedMsi @('/x', $First, '/qn', 'INLAID_TEST_FAIL_AFTER_FINALIZE_USER_PATH_MARKER=1') 'Injected failed uninstall from originally present-empty PATH' 'present-empty-path-failed-uninstall.log'
    Assert-PostFinalizeUserPathMarkerFailureLog 'present-empty-path-failed-uninstall.log'
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Assert-ProductAbsent $SecondProductCode
    Assert-ExactUserPathState $true $InstallRoot ([Microsoft.Win32.RegistryValueKind]::ExpandString) 'Present-empty-PATH failed-uninstall rollback'
    Assert-Marker $true $InstallRoot $true
    if ((Get-ExactUserPathSnapshot) -cne $BeforePresentEmptyPathFailedUninstall.path -or
        (Get-MarkerSnapshot) -cne $BeforePresentEmptyPathFailedUninstall.marker -or
        (Get-InstallerRegistryStructureSnapshot) -cne $BeforePresentEmptyPathFailedUninstall.installerStructure -or
        (Get-InstallTreeSnapshot) -cne $BeforePresentEmptyPathFailedUninstall.installTree -or
        (Get-InlaidRegistrationSnapshot) -cne $BeforePresentEmptyPathFailedUninstall.registration -or
        (Get-TransactionFilesSnapshot $LifecycleProductCodes) -cne $BeforePresentEmptyPathFailedUninstall.transactionFiles) {
        throw 'Failed uninstall from originally present-empty PATH did not restore product registration, payload, marker, PATH, and transaction files exactly.'
    }
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Present-empty-PATH failed-uninstall rollback'
    Save-LifecycleSnapshot '09-present-empty-path-failed-uninstall'
    Invoke-Msi @('/x', $First, '/qn') 'Present-empty-PATH uninstall' 'present-empty-path-uninstall.log'
    Assert-ActionSuccessLog 'present-empty-path-uninstall.log' 'FinalizeUserPathMarker'
    Assert-SplitUninstallRegistrationLog 'present-empty-path-uninstall.log'
    $InstalledMsi = ''
    Assert-NoInlaidRegistration
    Assert-ExactUserPathState $true '' ([Microsoft.Win32.RegistryValueKind]::ExpandString) 'Present-empty-PATH uninstall restore'
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Present-empty-PATH uninstall restore'
    Assert-InstallerRegistryStructureEmpty 'Present-empty-PATH uninstall'
    Save-LifecycleSnapshot '10-present-empty-path-uninstall'
    Restore-OriginalUserPath
    Assert-OriginalUserPath 'Original PATH restore after absent and present-empty PATH cases'

    $InvalidProgramDir = 'C:\Inlaid;Rejected'
    Assert-FailedMsi @('/i', $First, '/qn', "INLAID_PATH_PROGRAM_DIR=$InvalidProgramDir") 'Semicolon clean install' 'semicolon-install.log'
    Assert-ActionFailureLog 'semicolon-install.log' 'ApplyUserPath'
    if ((Test-Path -LiteralPath $InstallRoot) -or (Get-ExactUserPath) -cne $OriginalPath) {
        throw 'Semicolon clean install committed files, PATH, or provenance state.'
    }
    Assert-InstallerRegistryStructureEmpty 'Semicolon clean-install rollback'
    Assert-OriginalUserPath 'Semicolon clean-install rollback'
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Semicolon clean-install rollback'

    $quotedEquivalent = $OriginalPath + $(if ($OriginalPath.Length -eq 0) { '' } else { ';' }) + '"' + $InstallRoot + '\"'
    Set-ExactUserPath $quotedEquivalent
    Invoke-Msi @('/i', $First, '/qn') 'Equivalent-collision install' 'collision-install.log'
    $InstalledMsi = $First
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Assert-ExactUserPathState $true $quotedEquivalent $OriginalPathKind 'Equivalent-collision install'
    Assert-Marker $false '' $false
    Invoke-Msi @('/x', $First, '/qn') 'Equivalent-collision uninstall' 'collision-uninstall.log'
    $InstalledMsi = ''
    Assert-NoInlaidRegistration
    if ((Get-ExactUserPath) -cne $quotedEquivalent) { throw 'Uninstall changed user-owned equivalent PATH text.' }
    Assert-InstallerRegistryStructureEmpty 'Equivalent-collision uninstall'
    Assert-ExactUserPathState $true $quotedEquivalent $OriginalPathKind 'Equivalent-collision uninstall'
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Equivalent-collision uninstall'
    Restore-OriginalUserPath
    Assert-OriginalUserPath 'Equivalent-collision original restore'

    Invoke-Msi @('/i', $First, '/qb!') 'Basic-UI clean per-user install' 'basic-install.log'
    Assert-BasicUiLog 'basic-install.log'
    $InstalledMsi = $First
    Assert-ExactUserPathState $true (Get-ExpectedAppend $OriginalPath) $OwnedPathKind 'Basic-UI clean install'
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Invoke-Msi @('/x', $First, '/qn') 'Basic-UI lifecycle uninstall' 'basic-uninstall.log'
    $InstalledMsi = ''
    Assert-NoInlaidRegistration
    Assert-InstallerRegistryStructureEmpty 'Basic-UI uninstall'
    Restore-OriginalUserPath
    Assert-OriginalUserPath 'Basic-UI uninstall restore'
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Basic-UI uninstall restore'

    Invoke-Msi @('/i', $First, '/qn') 'Unattended clean per-user install' 'clean-install.log'
    $InstalledMsi = $First
    $ExpectedOwnedPath = Get-ExpectedAppend $OriginalPath
    Assert-ExactUserPathState $true $ExpectedOwnedPath $OwnedPathKind 'Unattended clean install'
    Assert-Marker $true $InstallRoot $PathPresent
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence

    foreach ($path in $UserData) {
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $path) | Out-Null
        Set-Content -LiteralPath $path -Value 'retain through lifecycle' -Encoding utf8
    }
    $ExpectedUserDataSnapshot = Get-UserDataSnapshot
    Save-LifecycleSnapshot '10-clean-install-with-user-data'

    Restore-OriginalUserPath
    Invoke-Msi @('/fa', $First, '/qn') 'Repair missing owned PATH segment' 'repair-missing.log'
    Assert-ExactUserPathState $true $ExpectedOwnedPath $OwnedPathKind 'Repair missing owned PATH segment'
    Assert-Marker $true $InstallRoot $PathPresent
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence

    $EditedEquivalent = $OriginalPath + $(if ($OriginalPath.Length -eq 0) { '' } else { ';' }) + '"' + $InstallRoot + '\"'
    Set-ExactUserPath $EditedEquivalent
    Invoke-Msi @('/fa', $First, '/qn') 'Repair user-edited equivalent PATH segment' 'repair-edited.log'
    Assert-ExactUserPathState $true $EditedEquivalent $OriginalPathKind 'Repair user-edited equivalent PATH segment'
    Assert-Marker $false '' $false
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence

    Restore-OriginalUserPath
    Invoke-Msi @('/fa', $First, '/qn') 'Repair after user-owned equivalent removal' 'repair-reown.log'
    Assert-Marker $true $InstallRoot $PathPresent
    Assert-ExactUserPathState $true $ExpectedOwnedPath $OwnedPathKind 'Repair PATH ownership reacquisition'
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence

    $BeforeSemicolonRepairPath = Get-ExactUserPathSnapshot
    $BeforeSemicolonRepairMarker = Get-MarkerSnapshot
    Assert-FailedMsi @('/fa', $First, '/qn', "INLAID_PATH_PROGRAM_DIR=$InvalidProgramDir") 'Semicolon repair' 'semicolon-repair.log'
    Assert-ActionFailureLog 'semicolon-repair.log' 'ApplyUserPath'
    if ((Get-ExactUserPathSnapshot) -cne $BeforeSemicolonRepairPath -or
        (Get-MarkerSnapshot) -cne $BeforeSemicolonRepairMarker) {
        throw 'Semicolon repair changed PATH text, value kind, or provenance state.'
    }
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Semicolon repair rollback'
    Assert-UserData
    Save-LifecycleSnapshot '11-repair-and-semicolon-rollback'

    $BeforeUpgradePath = Get-ExactUserPathSnapshot
    $BeforeUpgradeMarker = Get-MarkerSnapshot
    $BeforeUpgradePayload = Get-InstallTreeSnapshot
    $BeforeUpgradeRegistration = Get-InlaidRegistrationSnapshot
    Assert-FailedMsi @('/i', $Second, '/qn', "INLAID_PATH_PROGRAM_DIR=$InvalidProgramDir") 'Semicolon major upgrade' 'semicolon-upgrade.log'
    Assert-ActionFailureLog 'semicolon-upgrade.log' 'ApplyUserPath'
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    if ((Get-ExactUserPathSnapshot) -cne $BeforeUpgradePath -or
        (Get-MarkerSnapshot) -cne $BeforeUpgradeMarker) {
        throw 'Semicolon upgrade changed PATH text, value kind, or provenance state.'
    }
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Semicolon upgrade rollback'

    Assert-FailedMsi @('/i', $Second, '/qn', 'INLAID_TEST_INJECT_FAILURE=1') 'Injected failed major upgrade' 'failed-upgrade.log'
    Assert-ActionFailureLog 'failed-upgrade.log' 'FailAfterUserPath'
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Assert-ProductAbsent $SecondProductCode
    Assert-Marker $true $InstallRoot $PathPresent
    if ((Get-ExactUserPathSnapshot) -cne $BeforeUpgradePath -or
        (Get-MarkerSnapshot) -cne $BeforeUpgradeMarker -or
        (Get-InstallTreeSnapshot) -cne $BeforeUpgradePayload -or
        (Get-InlaidRegistrationSnapshot) -cne $BeforeUpgradeRegistration) {
        throw 'Failed upgrade before RemoveExistingProducts did not restore PATH kind/text, provenance, registration, and payload exactly.'
    }
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Failed upgrade before RemoveExistingProducts'
    Assert-UserData
    Save-LifecycleSnapshot '12-failed-upgrade-before-remove-existing-products'

    Assert-FailedMsi @('/i', $Second, '/qn', 'INLAID_TEST_FAIL_AFTER_REMOVE_EXISTING_PRODUCTS=1') 'Injected failed major upgrade after RemoveExistingProducts' 'failed-upgrade-after-remove-existing-products.log'
    Assert-PostRemoveExistingProductsFailureLog 'failed-upgrade-after-remove-existing-products.log'
    Assert-InstalledPayload $FirstVersion $FirstMsiVersion $FirstProductCode $FirstEvidence
    Assert-ProductAbsent $SecondProductCode
    Assert-Marker $true $InstallRoot $PathPresent
    if ((Get-ExactUserPathSnapshot) -cne $BeforeUpgradePath -or
        (Get-MarkerSnapshot) -cne $BeforeUpgradeMarker -or
        (Get-InstallTreeSnapshot) -cne $BeforeUpgradePayload -or
        (Get-InlaidRegistrationSnapshot) -cne $BeforeUpgradeRegistration) {
        throw 'Failed upgrade after RemoveExistingProducts did not restore PATH kind/text, provenance, registration, and payload exactly.'
    }
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Failed upgrade after RemoveExistingProducts'
    Assert-UserData
    Save-LifecycleSnapshot '13-failed-upgrade-after-remove-existing-products'

    Invoke-Msi @('/i', $Second, '/qn') 'Major upgrade' 'upgrade.log'
    $InstalledMsi = $Second
    Assert-InstalledPayload $SecondVersion $SecondMsiVersion $SecondProductCode $SecondEvidence
    Assert-Marker $true $InstallRoot $PathPresent
    Assert-ExactUserPathState $true $ExpectedOwnedPath $OwnedPathKind 'Successful major upgrade'
    Assert-ProductAbsent $FirstProductCode
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Successful major upgrade'
    Assert-UserData
    Save-LifecycleSnapshot '14-successful-upgrade'

    Assert-FailedMsi @('/i', $First, '/qn') 'Downgrade' 'downgrade.log'
    Assert-ActionFailureLog 'downgrade.log' 'LaunchConditions' 'A newer version of Inlaid is already installed.'
    Assert-InstalledPayload $SecondVersion $SecondMsiVersion $SecondProductCode $SecondEvidence
    Assert-UserData
    Save-LifecycleSnapshot '15-blocked-downgrade'

    $CollisionDirectory = Join-Path $TemporaryRoot 'collision'
    New-Item -ItemType Directory -Path $CollisionDirectory | Out-Null
    $CollisionCommand = Join-Path $CollisionDirectory 'inlaid.cmd'
    Set-Content -LiteralPath $CollisionCommand -Value '@echo foreign-inlaid' -Encoding ascii
    $env:PATH = $CollisionDirectory + ';' + $InstallRoot + ';' + [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $BeforeCollisionRepairPath = Get-ExactUserPath
    Invoke-Msi @('/fa', $Second, '/qn') 'Repair with a foreign effective-PATH collision' 'collision-repair.log'
    if ((Get-ExactUserPath) -cne $BeforeCollisionRepairPath -or
        -not (Test-Path -LiteralPath $CollisionCommand -PathType Leaf)) {
        throw 'Repair changed PATH ownership or deleted a foreign command collision.'
    }
    Assert-ExactUserPathState $true $BeforeCollisionRepairPath $OwnedPathKind 'Collision repair'
    Assert-InstalledPayload $SecondVersion $SecondMsiVersion $SecondProductCode $SecondEvidence
    $whereCollision = @(where.exe inlaid)
    $shellCollision = (Get-Command inlaid -CommandType Application | Select-Object -First 1).Source
    if ($whereCollision.Count -lt 2 -or $whereCollision[0] -cne $CollisionCommand -or $shellCollision -cne $CollisionCommand) {
        throw 'PATH collision evidence did not resolve the foreign command first.'
    }
    if (-not (Test-Path -LiteralPath $CollisionCommand -PathType Leaf)) { throw 'Installer deleted a foreign PATH collision.' }
    $env:PATH = $OriginalProcessPath

    Invoke-Msi @('/x', $Second, '/qn') 'Final uninstall' 'uninstall.log'
    Assert-ActionSuccessLog 'uninstall.log' 'FinalizeUserPathMarker'
    Assert-SplitUninstallRegistrationLog 'uninstall.log'
    $InstalledMsi = ''
    Assert-NoInlaidRegistration
    if (Test-Path -LiteralPath $InstallRoot) { throw 'Uninstall retained the MSI-owned program directory.' }
    Assert-InstallerRegistryStructureEmpty 'Final uninstall'
    Assert-OriginalUserPath 'Final uninstall restore'
    Assert-TransactionSnapshotsAbsent -ProductCodes $LifecycleProductCodes -Context 'Final uninstall restore'
    if (-not (Test-Path -LiteralPath $CollisionCommand -PathType Leaf)) { throw 'Uninstall deleted a foreign command collision.' }
    Assert-UserData
    Save-LifecycleSnapshot '99-final-uninstall'
    $InitializationPhase = 'lifecycle complete'
    $LifecyclePassed = $true
}
catch {
    $LifecycleFailure = $_
}
finally {
    try { Write-LifecycleStateEvidence 'pre-cleanup' $LifecycleFailure }
    catch { $CleanupErrors += "pre-cleanup state evidence: $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
    if ($null -ne (Get-Command Retain-LifecycleEvidence -CommandType Function -ErrorAction SilentlyContinue)) {
        try {
            [void](Retain-LifecycleEvidence $(if ($LifecyclePassed) { 'passed' } else { 'failed' }) 'pre-cleanup')
            $LifecycleEvidenceRetained = $true
        }
        catch { $CleanupErrors += "pre-cleanup artifact retention: $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
    }
    $env:PATH = $OriginalProcessPath
    if (-not $MsiClientTimedOut) {
        if (-not [string]::IsNullOrWhiteSpace($InstalledMsi) -and
            $null -ne (Get-Command Invoke-MsiExitCode -CommandType Function -ErrorAction SilentlyContinue)) {
            try {
                $cleanupExitCode = Invoke-MsiExitCode @('/x', $InstalledMsi, '/qn') 'cleanup-uninstall.log'
                if ($cleanupExitCode -ne 0) { throw "cleanup uninstall returned Windows Installer exit code $cleanupExitCode" }
            }
            catch { $CleanupErrors += "cleanup uninstall: $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
        }
        if (-not $MsiClientTimedOut -and $OriginalPathCaptured -and
            $null -ne (Get-Command Restore-OriginalUserPath -CommandType Function -ErrorAction SilentlyContinue)) {
            try { Restore-OriginalUserPath }
            catch { $CleanupErrors += "restore original PATH: $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
        }
        if (-not $MsiClientTimedOut -and
            $null -ne (Get-Command Assert-InstallerRegistryStructureEmpty -CommandType Function -ErrorAction SilentlyContinue)) {
            try { Assert-InstallerRegistryStructureEmpty 'Lifecycle cleanup' }
            catch {
                $CleanupErrors += "registry cleanup evidence: $($_.Exception.Message)"
                Write-Warning "Lifecycle cleanup preserved non-empty installer registry structure for investigation: $($_.Exception.Message)"
            }
        }
        if (-not $MsiClientTimedOut -and -not [string]::IsNullOrWhiteSpace($ExpectedUserDataSnapshot) -and
            $null -ne (Get-Command Get-UserDataSnapshot -CommandType Function -ErrorAction SilentlyContinue)) {
            try {
                if ((Get-UserDataSnapshot) -cne $ExpectedUserDataSnapshot) {
                    throw 'Known user-data fixtures no longer match their harness-owned inventory; preserving them for investigation.'
                }
                foreach ($path in $UserData) { Remove-Item -LiteralPath $path -Force -ErrorAction Stop }
            }
            catch { $CleanupErrors += "remove exact owned user-data fixtures: $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
        }
    }
    else {
        Write-Warning $CleanupSuppressedReason
    }
    $postCleanupPhase = if ($MsiClientTimedOut) { 'post-cleanup-suppressed-service-unknown' } else { 'post-cleanup' }
    try { Write-LifecycleStateEvidence $postCleanupPhase $LifecycleFailure }
    catch { $CleanupErrors += "post-cleanup state evidence: $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
    try {
        if ($null -ne (Get-Command Retain-LifecycleEvidence -CommandType Function -ErrorAction SilentlyContinue)) {
            [void](Retain-LifecycleEvidence $(if ($LifecyclePassed) { 'passed' } else { 'failed' }) $postCleanupPhase)
            $LifecycleEvidenceRetained = $true
        }
    }
    catch {
        $CleanupErrors += "retain lifecycle evidence: $($_.Exception.Message)"
        Write-Warning "Could not copy bounded MSI lifecycle evidence; original lifecycle outcome is preserved: $($_.Exception.Message)"
    }
    try { if ($null -ne $EnvironmentKey) { $EnvironmentKey.Close() } }
    catch { $CleanupErrors += "close Environment key: $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
    if ($LifecyclePassed -and $LifecycleEvidenceRetained -and -not $MsiClientTimedOut -and -not [string]::IsNullOrWhiteSpace($RetainedLifecycleEvidence) -and
        (Test-Path -LiteralPath $TemporaryRoot -PathType Container)) {
        foreach ($diagnostic in @(Get-TestFailureDiagnosticFiles $LifecycleProductCodes)) {
            $diagnosticPath = [System.IO.Path]::GetFullPath($diagnostic.FullName)
            if (-not $RetainedHelperDiagnosticPaths.Contains($diagnosticPath)) { continue }
            try { Remove-Item -LiteralPath $diagnosticPath -Force -ErrorAction Stop }
            catch { $CleanupErrors += "remove retained helper diagnostic $($diagnostic.FullName): $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
        }
        $resolved = [System.IO.Path]::GetFullPath($TemporaryRoot)
        if ($resolved.StartsWith($TemporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) {
            try { Remove-Item -LiteralPath $resolved -Recurse -Force }
            catch { $CleanupErrors += "remove successful lifecycle working directory: $($_.Exception.Message)"; Write-Warning $CleanupErrors[-1] }
        }
    }
    elseif (Test-Path -LiteralPath $TemporaryRoot -PathType Container) {
        Write-Warning "Retained failed MSI lifecycle working evidence: $TemporaryRoot; copied evidence: $RetainedLifecycleEvidence"
    }
}

if ($null -ne $LifecycleFailure) {
    $PSCmdlet.ThrowTerminatingError($LifecycleFailure)
}
if ($CleanupErrors.Count -ne 0) {
    throw "MSI lifecycle completed but cleanup/evidence retention failed: $($CleanupErrors -join '; ')"
}

Write-Host 'Windows MSI terminal-first lifecycle, PATH ownership, rollback, collision, and preservation tests passed.' -ForegroundColor Green
Write-Host "Retained lifecycle logs and evidence: $RetainedLifecycleEvidence"
