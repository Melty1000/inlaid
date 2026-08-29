[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Wix,
    [ValidateRange(60, 2700)][int]$ChildTimeoutSeconds = 2400,
    [switch]$AcceptLocalUserSetup,
    [switch]$AcceptMachinePolicyOverride
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if (-not $AcceptLocalUserSetup) {
    throw 'This wrapper creates and removes a temporary local user and profile. Re-run with -AcceptLocalUserSetup only on an authorized disposable GitHub-hosted Windows runner.'
}
if ($env:GITHUB_ACTIONS -cne 'true' -or $env:RUNNER_ENVIRONMENT -cne 'github-hosted' -or $env:RUNNER_OS -cne 'Windows') {
    throw 'Temporary local-user setup is restricted to a GitHub-hosted Windows Actions runner.'
}

$parentIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
$parentPrincipal = [Security.Principal.WindowsPrincipal]::new($parentIdentity)
if (-not $parentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Temporary local-user setup requires the administrator token provided by a GitHub-hosted Windows runner.'
}

$ProjectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$LifecycleScript = Join-Path $PSScriptRoot 'test-windows-msi.ps1'
$ChildScript = Join-Path $PSScriptRoot 'test-windows-msi-standard-user-child.ps1'
$WixPath = if ([System.IO.Path]::IsPathRooted($Wix)) {
    [System.IO.Path]::GetFullPath($Wix)
} else {
    [System.IO.Path]::GetFullPath((Join-Path $ProjectRoot $Wix))
}
if (-not (Test-Path -LiteralPath $LifecycleScript -PathType Leaf)) { throw "Lifecycle script not found: $LifecycleScript" }
if (-not (Test-Path -LiteralPath $ChildScript -PathType Leaf)) { throw "Standard-user child script not found: $ChildScript" }
if (-not (Test-Path -LiteralPath $WixPath -PathType Leaf)) { throw "WiX executable not found: $WixPath" }

$PackageEvidenceRoot = Join-Path $ProjectRoot '.tools\evidence\windows-msi-package'
$HelperEvidenceRoot = Join-Path $ProjectRoot '.tools\evidence\windows-msi-helper'
$PackageEvidenceProbeFile = Get-ChildItem -LiteralPath $PackageEvidenceRoot -File -Recurse -ErrorAction SilentlyContinue | Sort-Object FullName | Select-Object -First 1
$HelperEvidenceProbeFile = Get-ChildItem -LiteralPath $HelperEvidenceRoot -File -Recurse -ErrorAction SilentlyContinue | Sort-Object FullName | Select-Object -First 1
if ($null -eq $PackageEvidenceProbeFile) { throw "Package evidence probe file not found beneath: $PackageEvidenceRoot" }
if ($null -eq $HelperEvidenceProbeFile) { throw "Focused-helper evidence probe file not found beneath: $HelperEvidenceRoot" }

$EvidenceRoot = Join-Path $ProjectRoot '.tools\evidence\windows-msi-standard-user'
$LifecycleEvidenceRoot = Join-Path $ProjectRoot '.tools\evidence\windows-msi-lifecycle'
$RunName = 'run ' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + ' ' + [Guid]::NewGuid().ToString('N').Substring(0, 8)
$EvidenceDirectory = Join-Path $EvidenceRoot $RunName
New-Item -ItemType Directory -Force -Path $EvidenceDirectory | Out-Null
New-Item -ItemType Directory -Force -Path $LifecycleEvidenceRoot | Out-Null

$RunId = [Guid]::NewGuid().ToString('N')
$UserName = 'inlaid-ci-' + $RunId.Substring(0, 10)
$GatePath = Join-Path $EvidenceDirectory ('child-gate-' + $RunId)
$StandardOutputPath = Join-Path $EvidenceDirectory 'child-stdout.log'
$StandardErrorPath = Join-Path $EvidenceDirectory 'child-stderr.log'
$ConfigurationPath = Join-Path $EvidenceDirectory 'child-configuration.json'
$OrchestratorPath = Join-Path $EvidenceDirectory 'orchestrator.json'
$CleanupPath = Join-Path $EvidenceDirectory 'cleanup.json'
$ChildScriptHashBefore = ''
$ConfigurationHashBefore = ''
$ValidatedLifecycleEvidenceRun = ''
$ValidatedLifecycleRunJsonSHA256 = ''
$ValidatedLifecycleBootstrapSHA256 = ''
$ValidatedAccessBoundarySHA256 = ''
$InstallerPolicySubKey = 'SOFTWARE\Policies\Microsoft\Windows\Installer'
$InstallerPolicyParentSubKey = 'SOFTWARE\Policies\Microsoft\Windows'
$InstallerPolicyLeafName = 'Installer'
$InstallerPolicyNames = @('DisableMSI', 'DisableUserInstalls')
$InstallerPolicyBefore = @()
$InstallerPolicyEffective = @()
$InstallerPolicyAfter = @()
$InstallerPolicyCaptured = $false
$InstallerPolicyMutationRequired = $false
$InstallerPolicyOverrideApplied = $false
$InstallerPolicyChangedNames = @()
$InstallerPolicyAppliedNames = @()
$InstallerPolicyKeyCreated = $false
$InstallerPolicyKeyAfter = $null
$InstallerPolicyRestorationAttempted = $false
$InstallerPolicyRestored = $null
$Password = $null
$Credential = $null
$User = $null
$UserSid = $null
$ProjectAccessRule = $null
$ProjectDenyRule = $null
$EvidenceAccessRule = $null
$OriginalProjectAcl = $null
$OriginalEvidenceAcl = $null
$OriginalLifecycleEvidenceAcl = $null
$OriginalProjectAccessSddl = ''
$OriginalEvidenceAccessSddl = ''
$OriginalLifecycleEvidenceAccessSddl = ''
$Process = $null
$JobHandle = [IntPtr]::Zero
$ProcessAssignedToJob = $false
$ChildExitCode = $null
$TimedOut = $false
$PreserveDisposableState = $false
$PreservationReasons = @()
$ChildEvidenceValidated = $false
$Failure = $null
$CleanupErrors = @()

function New-RandomSecurePassword {
    $alphabet = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()-_=+'.ToCharArray()
    $characters = New-Object char[] 40
    $required = @('a', 'A', '7', '!')
    for ($index = 0; $index -lt $required.Count; $index++) { $characters[$index] = $required[$index] }
    $random = New-Object byte[] ($characters.Length - $required.Count)
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    $generator.GetBytes($random)
    for ($index = $required.Count; $index -lt $characters.Length; $index++) {
        $characters[$index] = $alphabet[$random[$index - $required.Count] % $alphabet.Length]
    }
    $generator.GetBytes($random)
    for ($index = $characters.Length - 1; $index -gt 0; $index--) {
        $swap = $random[$index % $random.Length] % ($index + 1)
        $temporary = $characters[$index]
        $characters[$index] = $characters[$swap]
        $characters[$swap] = $temporary
    }
    $secure = [Security.SecureString]::new()
    foreach ($character in $characters) { $secure.AppendChar($character) }
    $secure.MakeReadOnly()
    [Array]::Clear($characters, 0, $characters.Length)
    [Array]::Clear($random, 0, $random.Length)
    $generator.Dispose()
    return $secure
}

function Test-LocalGroupMembership([Security.Principal.SecurityIdentifier]$Sid, [string]$GroupSid) {
    $group = Get-LocalGroup -SID $GroupSid
    return $null -ne (Get-LocalGroupMember -Group $group | Where-Object { $_.SID -eq $Sid } | Select-Object -First 1)
}

function Get-MachineInstallerPolicyValue([string]$Name) {
    $baseKey = [Microsoft.Win32.RegistryKey]::OpenBaseKey(
        [Microsoft.Win32.RegistryHive]::LocalMachine,
        [Microsoft.Win32.RegistryView]::Registry64
    )
    $key = $null
    try {
        $key = $baseKey.OpenSubKey($InstallerPolicySubKey, $false)
        if ($null -eq $key) {
            return [pscustomobject][ordered]@{
                name = $Name
                keyPresent = $false
                present = $false
                kind = $null
                value = $null
            }
        }
        $matchingNames = @($key.GetValueNames() | Where-Object { $_ -ieq $Name })
        if ($matchingNames.Count -gt 1) { throw "Installer policy $Name has multiple case-insensitive registry matches." }
        if ($matchingNames.Count -eq 0) {
            return [pscustomobject][ordered]@{
                name = $Name
                keyPresent = $true
                present = $false
                kind = $null
                value = $null
            }
        }
        $actualName = [string]$matchingNames[0]
        $kind = $key.GetValueKind($actualName)
        $value = $key.GetValue($actualName, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        return [pscustomobject][ordered]@{
            name = $Name
            keyPresent = $true
            present = $true
            kind = $kind.ToString()
            value = [long]$value
        }
    }
    finally {
        if ($null -ne $key) { $key.Dispose() }
        $baseKey.Dispose()
    }
}

function Set-MachineInstallerPolicyDword([string]$Name, [uint32]$Value) {
    $baseKey = [Microsoft.Win32.RegistryKey]::OpenBaseKey(
        [Microsoft.Win32.RegistryHive]::LocalMachine,
        [Microsoft.Win32.RegistryView]::Registry64
    )
    $key = $null
    try {
        $key = $baseKey.OpenSubKey($InstallerPolicySubKey, $true)
        if ($null -eq $key) { throw "Installer policy key is missing: HKLM\$InstallerPolicySubKey" }
        $key.SetValue($Name, [int]$Value, [Microsoft.Win32.RegistryValueKind]::DWord)
    }
    finally {
        if ($null -ne $key) { $key.Dispose() }
        $baseKey.Dispose()
    }
}

function New-MachineInstallerPolicyKeyIfMissing {
    $baseKey = [Microsoft.Win32.RegistryKey]::OpenBaseKey(
        [Microsoft.Win32.RegistryHive]::LocalMachine,
        [Microsoft.Win32.RegistryView]::Registry64
    )
    $parentKey = $null
    try {
        $parentKey = $baseKey.OpenSubKey($InstallerPolicyParentSubKey, $true)
        if ($null -eq $parentKey) { throw "Installer policy parent key is missing: HKLM\$InstallerPolicyParentSubKey" }
        $parentHandle = $parentKey.SafeRegistryHandle.DangerousGetHandle()
        return [InlaidInstallerPolicyNative]::CreateNewKey($parentHandle, $InstallerPolicyLeafName)
    }
    finally {
        if ($null -ne $parentKey) { $parentKey.Dispose() }
        $baseKey.Dispose()
    }
}

function Remove-MachineInstallerPolicyValue([string]$Name) {
    $baseKey = [Microsoft.Win32.RegistryKey]::OpenBaseKey(
        [Microsoft.Win32.RegistryHive]::LocalMachine,
        [Microsoft.Win32.RegistryView]::Registry64
    )
    $key = $null
    try {
        $key = $baseKey.OpenSubKey($InstallerPolicySubKey, $true)
        if ($null -eq $key) { throw "Installer policy key disappeared before restoring absent value $Name." }
        $matchingNames = @($key.GetValueNames() | Where-Object { $_ -ieq $Name })
        if ($matchingNames.Count -ne 1) { throw "Temporary installer policy value $Name is not uniquely present for removal." }
        $actualName = [string]$matchingNames[0]
        if ($key.GetValueKind($actualName) -ne [Microsoft.Win32.RegistryValueKind]::DWord -or
            [long]$key.GetValue($actualName, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) -ne 0) {
            throw "Temporary installer policy value $Name changed before restoration."
        }
        $key.DeleteValue($actualName, $true)
    }
    finally {
        if ($null -ne $key) { $key.Dispose() }
        $baseKey.Dispose()
    }
}

function Get-MachineInstallerPolicyKeyInventory {
    $baseKey = [Microsoft.Win32.RegistryKey]::OpenBaseKey(
        [Microsoft.Win32.RegistryHive]::LocalMachine,
        [Microsoft.Win32.RegistryView]::Registry64
    )
    $key = $null
    try {
        $key = $baseKey.OpenSubKey($InstallerPolicySubKey, $false)
        if ($null -eq $key) {
            return [pscustomobject][ordered]@{
                present = $false
                valueNames = @()
                subKeyNames = @()
            }
        }
        return [pscustomobject][ordered]@{
            present = $true
            valueNames = @($key.GetValueNames() | Sort-Object)
            subKeyNames = @($key.GetSubKeyNames() | Sort-Object)
        }
    }
    finally {
        if ($null -ne $key) { $key.Dispose() }
        $baseKey.Dispose()
    }
}

function Test-InstallerPolicyValueSnapshotsEqual([object[]]$Expected, [object[]]$Actual) {
    $expectedValues = @($Expected | ForEach-Object {
        [pscustomobject][ordered]@{
            name = $_.name
            present = $_.present
            kind = $_.kind
            value = $_.value
        }
    })
    $actualValues = @($Actual | ForEach-Object {
        [pscustomobject][ordered]@{
            name = $_.name
            present = $_.present
            kind = $_.kind
            value = $_.value
        }
    })
    $expectedJson = ConvertTo-Json -InputObject $expectedValues -Depth 4 -Compress
    $actualJson = ConvertTo-Json -InputObject $actualValues -Depth 4 -Compress
    return $expectedJson -ceq $actualJson
}

function ConvertTo-QuotedWindowsProcessArgument([string]$Value) {
    if ($Value.Contains('"')) { throw 'A child process path contains an unsupported quote character.' }
    return '"' + $Value + '"'
}

function Write-OrchestratorEvidence([string]$Phase) {
    $administratorsMember = if ($null -eq $UserSid) { $null } else { Test-LocalGroupMembership $UserSid 'S-1-5-32-544' }
    $usersMember = if ($null -eq $UserSid) { $null } else { Test-LocalGroupMembership $UserSid 'S-1-5-32-545' }
    [ordered]@{
        schema = 1
        phase = $Phase
        createdUtc = [DateTime]::UtcNow.ToString('o')
        childTimeoutSeconds = $ChildTimeoutSeconds
        timedOut = $TimedOut
        childExitCode = $ChildExitCode
        preserveDisposableState = $PreserveDisposableState
        preservationReasons = @($PreservationReasons)
        execution = [ordered]@{
            childScript = $ChildScript
            childScriptSHA256Before = $ChildScriptHashBefore
            configuration = $ConfigurationPath
            configurationSHA256Before = $ConfigurationHashBefore
            validatedLifecycleEvidenceRun = $ValidatedLifecycleEvidenceRun
            validatedLifecycleRunJsonSHA256 = $ValidatedLifecycleRunJsonSHA256
            validatedLifecycleBootstrapSHA256 = $ValidatedLifecycleBootstrapSHA256
            validatedAccessBoundarySHA256 = $ValidatedAccessBoundarySHA256
        }
        machineInstallerPolicy = [ordered]@{
            registryView = 'Registry64'
            subKey = "HKLM\$InstallerPolicySubKey"
            explicitOverrideAuthorization = $AcceptMachinePolicyOverride.IsPresent
            captured = $InstallerPolicyCaptured
            mutationRequired = $InstallerPolicyMutationRequired
            overrideApplied = $InstallerPolicyOverrideApplied
            changedNames = @($InstallerPolicyChangedNames)
            appliedNames = @($InstallerPolicyAppliedNames)
            keyCreated = $InstallerPolicyKeyCreated
            before = @($InstallerPolicyBefore)
            effective = @($InstallerPolicyEffective)
        }
        account = [ordered]@{
            name = $UserName
            sid = if ($null -eq $UserSid) { $null } else { $UserSid.Value }
            usersGroupMember = $usersMember
            administratorsGroupMember = $administratorsMember
        }
        parent = [ordered]@{
            identity = $parentIdentity.Name
            sid = $parentIdentity.User.Value
            administratorRoleEnabledInCurrentToken = $parentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
        }
        runner = [ordered]@{
            runnerOS = $env:RUNNER_OS
            runnerArchitecture = $env:RUNNER_ARCH
            runnerEnvironment = $env:RUNNER_ENVIRONMENT
            runnerName = $env:RUNNER_NAME
            imageOS = $env:ImageOS
            imageVersion = $env:ImageVersion
            githubActions = $env:GITHUB_ACTIONS
            repository = $env:GITHUB_REPOSITORY
            workflow = $env:GITHUB_WORKFLOW
            job = $env:GITHUB_JOB
            runId = $env:GITHUB_RUN_ID
            runAttempt = $env:GITHUB_RUN_ATTEMPT
            sha = $env:GITHUB_SHA
        }
        failure = if ($null -eq $Failure) { $null } else { [ordered]@{
            message = $Failure.Exception.Message
            category = [string]$Failure.CategoryInfo
            fullyQualifiedErrorId = $Failure.FullyQualifiedErrorId
            scriptStackTrace = $Failure.ScriptStackTrace
        } }
    } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $OrchestratorPath -Encoding utf8NoBOM
}

$JobSource = @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

public static class InlaidStandardUserJob {
    private const UInt32 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x00002000;

    [StructLayout(LayoutKind.Sequential)]
    private struct IO_COUNTERS {
        public UInt64 ReadOperationCount, WriteOperationCount, OtherOperationCount;
        public UInt64 ReadTransferCount, WriteTransferCount, OtherTransferCount;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct JOBOBJECT_BASIC_LIMIT_INFORMATION {
        public Int64 PerProcessUserTimeLimit, PerJobUserTimeLimit;
        public UInt32 LimitFlags;
        public UIntPtr MinimumWorkingSetSize, MaximumWorkingSetSize;
        public UInt32 ActiveProcessLimit;
        public UIntPtr Affinity;
        public UInt32 PriorityClass, SchedulingClass;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct JOBOBJECT_EXTENDED_LIMIT_INFORMATION {
        public JOBOBJECT_BASIC_LIMIT_INFORMATION BasicLimitInformation;
        public IO_COUNTERS IoInfo;
        public UIntPtr ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct JOBOBJECT_BASIC_ACCOUNTING_INFORMATION {
        public Int64 TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime;
        public UInt32 TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses;
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr CreateJobObject(IntPtr attributes, string name);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool SetInformationJobObject(IntPtr job, int informationClass, IntPtr information, UInt32 length);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool QueryInformationJobObject(IntPtr job, int informationClass, IntPtr information, UInt32 length, IntPtr returnLength);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool TerminateJobObject(IntPtr job, UInt32 exitCode);
    [DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool CloseHandle(IntPtr handle);

    public static IntPtr CreateKillOnClose() {
        IntPtr job = CreateJobObject(IntPtr.Zero, null);
        if (job == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error(), "CreateJobObject");
        JOBOBJECT_EXTENDED_LIMIT_INFORMATION limits = new JOBOBJECT_EXTENDED_LIMIT_INFORMATION();
        limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
        int size = Marshal.SizeOf(typeof(JOBOBJECT_EXTENDED_LIMIT_INFORMATION));
        IntPtr buffer = Marshal.AllocHGlobal(size);
        try {
            Marshal.StructureToPtr(limits, buffer, false);
            if (!SetInformationJobObject(job, 9, buffer, (UInt32)size))
                throw new Win32Exception(Marshal.GetLastWin32Error(), "SetInformationJobObject");
            return job;
        }
        catch { CloseHandle(job); throw; }
        finally { Marshal.FreeHGlobal(buffer); }
    }

    public static void Assign(IntPtr job, IntPtr process) {
        if (!AssignProcessToJobObject(job, process))
            throw new Win32Exception(Marshal.GetLastWin32Error(), "AssignProcessToJobObject");
    }

    public static UInt32 ActiveProcessCount(IntPtr job) {
        int size = Marshal.SizeOf(typeof(JOBOBJECT_BASIC_ACCOUNTING_INFORMATION));
        IntPtr buffer = Marshal.AllocHGlobal(size);
        try {
            if (!QueryInformationJobObject(job, 1, buffer, (UInt32)size, IntPtr.Zero))
                throw new Win32Exception(Marshal.GetLastWin32Error(), "QueryInformationJobObject");
            return ((JOBOBJECT_BASIC_ACCOUNTING_INFORMATION)Marshal.PtrToStructure(buffer, typeof(JOBOBJECT_BASIC_ACCOUNTING_INFORMATION))).ActiveProcesses;
        }
        finally { Marshal.FreeHGlobal(buffer); }
    }

    public static void Terminate(IntPtr job, UInt32 exitCode) {
        if (!TerminateJobObject(job, exitCode))
            throw new Win32Exception(Marshal.GetLastWin32Error(), "TerminateJobObject");
    }
}

public static class InlaidInstallerPolicyNative {
    private const UInt32 REG_OPTION_NON_VOLATILE = 0;
    private const Int32 KEY_READ = 0x00020019;
    private const Int32 KEY_WRITE = 0x00020006;
    private const UInt32 REG_CREATED_NEW_KEY = 1;
    private const UInt32 REG_OPENED_EXISTING_KEY = 2;

    [DllImport("advapi32.dll", CharSet = CharSet.Unicode)]
    private static extern Int32 RegCreateKeyExW(
        IntPtr key,
        string subKey,
        Int32 reserved,
        string keyClass,
        UInt32 options,
        Int32 desiredAccess,
        IntPtr securityAttributes,
        out IntPtr result,
        out UInt32 disposition
    );

    [DllImport("advapi32.dll")]
    private static extern Int32 RegCloseKey(IntPtr key);

    public static bool CreateNewKey(IntPtr parentKey, string leafName) {
        IntPtr result;
        UInt32 disposition;
        Int32 error = RegCreateKeyExW(
            parentKey,
            leafName,
            0,
            null,
            REG_OPTION_NON_VOLATILE,
            KEY_READ | KEY_WRITE,
            IntPtr.Zero,
            out result,
            out disposition
        );
        if (error != 0)
            throw new Win32Exception(error, "RegCreateKeyExW");
        Int32 closeError = RegCloseKey(result);
        if (closeError != 0)
            throw new Win32Exception(closeError, "RegCloseKey");
        if (disposition == REG_CREATED_NEW_KEY) return true;
        if (disposition == REG_OPENED_EXISTING_KEY) return false;
        throw new InvalidOperationException("RegCreateKeyExW returned an unknown disposition.");
    }
}
'@

try {
    Import-Module Microsoft.PowerShell.LocalAccounts -ErrorAction Stop
    Add-Type -TypeDefinition $JobSource -Language CSharp

    $InstallerPolicyBefore = @($InstallerPolicyNames | ForEach-Object { Get-MachineInstallerPolicyValue $_ })
    $InstallerPolicyCaptured = $true
    foreach ($policy in $InstallerPolicyBefore) {
        if (-not [bool]$policy.present) {
            if ($policy.name -ceq 'DisableMSI') { $InstallerPolicyChangedNames += [string]$policy.name }
            continue
        }
        if ($policy.kind -cne 'DWord') { throw "Installer policy $($policy.name) has unsupported registry kind $($policy.kind)." }
        $allowedValues = if ($policy.name -ceq 'DisableMSI') { @(0L, 1L, 2L) } else { @(0L, 1L) }
        if ([long]$policy.value -notin $allowedValues) {
            throw "Installer policy $($policy.name) has unsupported DWORD value $($policy.value)."
        }
        if ([long]$policy.value -ne 0) { $InstallerPolicyChangedNames += [string]$policy.name }
    }
    $InstallerPolicyMutationRequired = $InstallerPolicyChangedNames.Count -ne 0
    if ($InstallerPolicyMutationRequired -and -not $AcceptMachinePolicyOverride) {
        throw "The GitHub-hosted runner blocks unmanaged standard-user MSI execution through: $($InstallerPolicyChangedNames -join ', '). Re-run with -AcceptMachinePolicyOverride only on this disposable runner."
    }
    $disableMsiBefore = @($InstallerPolicyBefore | Where-Object { $_.name -ceq 'DisableMSI' })
    if ($disableMsiBefore.Count -ne 1) { throw 'DisableMSI policy preflight did not produce exactly one result.' }
    if ($InstallerPolicyMutationRequired -and -not [bool]$disableMsiBefore[0].keyPresent) {
        $InstallerPolicyKeyCreated = New-MachineInstallerPolicyKeyIfMissing
        if (-not $InstallerPolicyKeyCreated) { throw 'Installer policy key appeared concurrently before the bounded override.' }
    }
    foreach ($name in $InstallerPolicyChangedNames) {
        Set-MachineInstallerPolicyDword $name 0
        $InstallerPolicyAppliedNames += $name
    }
    $InstallerPolicyEffective = @($InstallerPolicyNames | ForEach-Object { Get-MachineInstallerPolicyValue $_ })
    foreach ($name in $InstallerPolicyChangedNames) {
        $effective = @($InstallerPolicyEffective | Where-Object { $_.name -ceq $name })
        if ($effective.Count -ne 1 -or -not [bool]$effective[0].present -or
            $effective[0].kind -cne 'DWord' -or [long]$effective[0].value -ne 0) {
            throw "Installer policy $name did not reach the required nonblocking DWORD value 0."
        }
    }
    $InstallerPolicyOverrideApplied = $InstallerPolicyAppliedNames.Count -ne 0
    Write-OrchestratorEvidence 'installer-policy-ready'

    $Password = New-RandomSecurePassword
    $Credential = [Management.Automation.PSCredential]::new("$env:COMPUTERNAME\$UserName", $Password)
    $User = New-LocalUser -Name $UserName -Password $Password -AccountNeverExpires -PasswordNeverExpires -Description 'Disposable Inlaid MSI standard-user test account'
    $UserSid = $User.SID

    $usersGroup = Get-LocalGroup -SID 'S-1-5-32-545'
    if (-not (Test-LocalGroupMembership $UserSid 'S-1-5-32-545')) {
        Add-LocalGroupMember -Group $usersGroup -Member $User
    }
    if (Test-LocalGroupMembership $UserSid 'S-1-5-32-544') { throw 'The temporary account unexpectedly belongs to the local Administrators group.' }
    if (-not (Test-LocalGroupMembership $UserSid 'S-1-5-32-545')) { throw 'The temporary account is not a member of the local Users group.' }

    $OriginalProjectAcl = Get-Acl -LiteralPath $ProjectRoot
    $OriginalEvidenceAcl = Get-Acl -LiteralPath $EvidenceRoot
    $OriginalLifecycleEvidenceAcl = Get-Acl -LiteralPath $LifecycleEvidenceRoot
    $OriginalProjectAccessSddl = $OriginalProjectAcl.GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access)
    $OriginalEvidenceAccessSddl = $OriginalEvidenceAcl.GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access)
    $OriginalLifecycleEvidenceAccessSddl = $OriginalLifecycleEvidenceAcl.GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access)

    $ProjectAccessRule = [Security.AccessControl.FileSystemAccessRule]::new(
        $UserSid,
        [Security.AccessControl.FileSystemRights]'ReadAndExecute, Synchronize',
        [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit',
        [Security.AccessControl.PropagationFlags]::None,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $ProjectDenyRule = [Security.AccessControl.FileSystemAccessRule]::new(
        $UserSid,
        [Security.AccessControl.FileSystemRights]'WriteData, AppendData, WriteExtendedAttributes, WriteAttributes, Delete, DeleteSubdirectoriesAndFiles, ChangePermissions, TakeOwnership',
        [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit',
        [Security.AccessControl.PropagationFlags]::None,
        [Security.AccessControl.AccessControlType]::Deny
    )
    $EvidenceAccessRule = [Security.AccessControl.FileSystemAccessRule]::new(
        $UserSid,
        [Security.AccessControl.FileSystemRights]'Modify, Synchronize',
        [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit',
        [Security.AccessControl.PropagationFlags]::None,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $evidenceAcl = Get-Acl -LiteralPath $EvidenceRoot
    $evidenceAcl.SetAccessRuleProtection($true, $true)
    [void]$evidenceAcl.AddAccessRule($EvidenceAccessRule)
    Set-Acl -LiteralPath $EvidenceRoot -AclObject $evidenceAcl
    $lifecycleEvidenceAcl = Get-Acl -LiteralPath $LifecycleEvidenceRoot
    $lifecycleEvidenceAcl.SetAccessRuleProtection($true, $true)
    [void]$lifecycleEvidenceAcl.AddAccessRule($EvidenceAccessRule)
    Set-Acl -LiteralPath $LifecycleEvidenceRoot -AclObject $lifecycleEvidenceAcl
    $projectAcl = Get-Acl -LiteralPath $ProjectRoot
    [void]$projectAcl.AddAccessRule($ProjectAccessRule)
    [void]$projectAcl.AddAccessRule($ProjectDenyRule)
    Set-Acl -LiteralPath $ProjectRoot -AclObject $projectAcl

    Write-OrchestratorEvidence 'account-created'

    $childConfiguration = [ordered]@{
        runId = $RunId
        expectedSid = $UserSid.Value
        gatePath = $GatePath
        evidenceDirectory = $EvidenceDirectory
        projectRoot = $ProjectRoot
        lifecycleScript = $LifecycleScript
        lifecycleEvidenceRoot = $LifecycleEvidenceRoot
        wixPath = $WixPath
        accessBoundary = [ordered]@{
            sourceExistingFile = $ChildScript
            rootCreatePath = Join-Path $ProjectRoot ('.inlaid-standard-user-root-probe-' + $RunId)
            packageExistingFile = $PackageEvidenceProbeFile.FullName
            packageCreatePath = Join-Path $PackageEvidenceRoot ('.inlaid-standard-user-package-probe-' + $RunId)
            helperExistingFile = $HelperEvidenceProbeFile.FullName
            helperCreatePath = Join-Path $HelperEvidenceRoot ('.inlaid-standard-user-helper-probe-' + $RunId)
            wrapperAllowedPath = Join-Path $EvidenceDirectory ('allowed-wrapper-probe-' + $RunId)
            lifecycleAllowedPath = Join-Path $LifecycleEvidenceRoot ('allowed-lifecycle-probe-' + $RunId)
        }
        toolEnvironment = [ordered]@{
            PATH = $env:PATH
            SystemRoot = $env:SystemRoot
            windir = $env:windir
            ComSpec = $env:ComSpec
            PATHEXT = $env:PATHEXT
            ProgramFiles = $env:ProgramFiles
            'ProgramFiles(x86)' = ${env:ProgramFiles(x86)}
            ProgramW6432 = $env:ProgramW6432
            PROCESSOR_ARCHITECTURE = $env:PROCESSOR_ARCHITECTURE
            PROCESSOR_IDENTIFIER = $env:PROCESSOR_IDENTIFIER
            NUMBER_OF_PROCESSORS = $env:NUMBER_OF_PROCESSORS
        }
        runnerFacts = [ordered]@{
            GITHUB_ACTIONS = $env:GITHUB_ACTIONS
            GITHUB_REPOSITORY = $env:GITHUB_REPOSITORY
            GITHUB_WORKFLOW = $env:GITHUB_WORKFLOW
            GITHUB_JOB = $env:GITHUB_JOB
            GITHUB_RUN_ID = $env:GITHUB_RUN_ID
            GITHUB_RUN_ATTEMPT = $env:GITHUB_RUN_ATTEMPT
            GITHUB_SHA = $env:GITHUB_SHA
            GITHUB_REF = $env:GITHUB_REF
            GITHUB_WORKSPACE = $env:GITHUB_WORKSPACE
            RUNNER_OS = $env:RUNNER_OS
            RUNNER_ARCH = $env:RUNNER_ARCH
            RUNNER_ENVIRONMENT = $env:RUNNER_ENVIRONMENT
            RUNNER_NAME = $env:RUNNER_NAME
            ImageOS = $env:ImageOS
            ImageVersion = $env:ImageVersion
        }
    }
    $childConfiguration | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $ConfigurationPath -Encoding utf8NoBOM
    $ChildScriptHashBefore = (Get-FileHash -LiteralPath $ChildScript -Algorithm SHA256).Hash
    $ConfigurationHashBefore = (Get-FileHash -LiteralPath $ConfigurationPath -Algorithm SHA256).Hash
    Write-OrchestratorEvidence 'launch-ready'
    $powerShellPath = (Get-Process -Id $PID).Path

    $Process = Start-Process -FilePath $powerShellPath -ArgumentList @(
        '-NoLogo', '-NoProfile', '-NonInteractive', '-File',
        (ConvertTo-QuotedWindowsProcessArgument $ChildScript),
        '-ConfigurationPath', (ConvertTo-QuotedWindowsProcessArgument $ConfigurationPath)
    ) -Credential $Credential -LoadUserProfile -UseNewEnvironment -WindowStyle Hidden -WorkingDirectory $ProjectRoot -RedirectStandardOutput $StandardOutputPath -RedirectStandardError $StandardErrorPath -PassThru

    $JobHandle = [InlaidStandardUserJob]::CreateKillOnClose()
    [InlaidStandardUserJob]::Assign($JobHandle, $Process.Handle)
    $ProcessAssignedToJob = $true
    Set-Content -LiteralPath $GatePath -Value 'released' -Encoding ascii

    $deadline = [DateTime]::UtcNow.AddSeconds($ChildTimeoutSeconds)
    while (-not $Process.HasExited) {
        $remaining = $deadline - [DateTime]::UtcNow
        if ($remaining.TotalMilliseconds -le 0) {
            $TimedOut = $true
            $PreserveDisposableState = $true
            $PreservationReasons += "The outer standard-user child timeout of $ChildTimeoutSeconds seconds expired."
            [InlaidStandardUserJob]::Terminate($JobHandle, 124)
            if (-not $Process.WaitForExit(30000)) { throw 'The terminated standard-user process tree did not settle within 30 seconds.' }
            throw "Standard-user MSI lifecycle exceeded its outer timeout of $ChildTimeoutSeconds seconds; its job-contained process tree was terminated."
        }
        $waitMilliseconds = [int][Math]::Min(30000, [Math]::Max(1, [Math]::Ceiling($remaining.TotalMilliseconds)))
        if ($Process.WaitForExit($waitMilliseconds)) { break }
        Write-Host "Standard-user MSI lifecycle is still running (child PID $($Process.Id)); bounded deadline $($deadline.ToString('o'))."
    }
    $ChildExitCode = $Process.ExitCode

    $treeDeadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([InlaidStandardUserJob]::ActiveProcessCount($JobHandle) -ne 0 -and [DateTime]::UtcNow -lt $treeDeadline) {
        Start-Sleep -Milliseconds 200
    }
    if ([InlaidStandardUserJob]::ActiveProcessCount($JobHandle) -ne 0) {
        $PreserveDisposableState = $true
        $PreservationReasons += 'A job-contained descendant did not exit within the bounded 30-second drain.'
        [InlaidStandardUserJob]::Terminate($JobHandle, 125)
        throw 'The lifecycle root exited while a job-contained descendant remained; the remaining process tree was terminated.'
    }

    $childTokenPath = Join-Path $EvidenceDirectory 'child-token-and-runner.json'
    $childCompletionPath = Join-Path $EvidenceDirectory 'child-completion.json'
    try {
        if (-not (Test-Path -LiteralPath $childTokenPath -PathType Leaf)) { throw 'Child token evidence is missing.' }
        if (-not (Test-Path -LiteralPath $childCompletionPath -PathType Leaf)) { throw 'Child completion evidence is missing.' }
        $childToken = Get-Content -Raw -LiteralPath $childTokenPath | ConvertFrom-Json
        $childCompletion = Get-Content -Raw -LiteralPath $childCompletionPath | ConvertFrom-Json
    }
    catch {
        $PreserveDisposableState = $true
        $PreservationReasons += "Child evidence could not be established: $($_.Exception.Message)"
        throw
    }
    if ([bool]$childCompletion.preserveDisposableState) {
        $PreserveDisposableState = $true
        $PreservationReasons += @($childCompletion.preservationReasons | ForEach-Object { [string]$_ })
    }
    $childScriptHashAfter = (Get-FileHash -LiteralPath $ChildScript -Algorithm SHA256).Hash
    $configurationHashAfter = (Get-FileHash -LiteralPath $ConfigurationPath -Algorithm SHA256).Hash
    if ($childScriptHashAfter -cne $ChildScriptHashBefore -or $configurationHashAfter -cne $ConfigurationHashBefore) {
        throw 'The child script or its nonsecret configuration changed across execution.'
    }
    foreach ($executionEvidence in @($childToken.execution, $childCompletion.execution)) {
        if ($executionEvidence.childScript -cne $ChildScript -or $executionEvidence.childScriptSHA256 -cne $ChildScriptHashBefore) {
            throw 'Child evidence does not identify the exact pre-launch child script.'
        }
        if ($executionEvidence.configuration -cne $ConfigurationPath -or $executionEvidence.configurationSHA256 -cne $ConfigurationHashBefore) {
            throw 'Child evidence does not identify the exact pre-launch configuration.'
        }
    }
    $accessBoundaryPath = Join-Path $EvidenceDirectory 'child-access-boundary.json'
    if (-not (Test-Path -LiteralPath $accessBoundaryPath -PathType Leaf)) { throw 'Child access-boundary evidence is missing.' }
    $ValidatedAccessBoundarySHA256 = (Get-FileHash -LiteralPath $accessBoundaryPath -Algorithm SHA256).Hash
    if ($childToken.execution.accessBoundary -cne $accessBoundaryPath -or $childCompletion.execution.accessBoundary -cne $accessBoundaryPath -or
        $childToken.execution.accessBoundarySHA256 -cne $ValidatedAccessBoundarySHA256 -or $childCompletion.execution.accessBoundarySHA256 -cne $ValidatedAccessBoundarySHA256) {
        throw 'Child token/completion evidence does not link the exact access-boundary evidence file.'
    }
    $accessBoundary = Get-Content -Raw -LiteralPath $accessBoundaryPath | ConvertFrom-Json
    if ($accessBoundary.runId -cne $RunId -or $accessBoundary.sid -cne $UserSid.Value -or $accessBoundary.expectedSid -cne $UserSid.Value) {
        throw 'Child access-boundary evidence does not match the wrapper run or temporary SID.'
    }
    if ($accessBoundary.execution.childScript -cne $ChildScript -or $accessBoundary.execution.childScriptSHA256 -cne $ChildScriptHashBefore -or
        $accessBoundary.execution.configuration -cne $ConfigurationPath -or $accessBoundary.execution.configurationSHA256 -cne $ConfigurationHashBefore) {
        throw 'Child access-boundary evidence does not identify the exact child/configuration seam.'
    }
    if ($accessBoundary.probes.sourceExisting.path -cne $childConfiguration.accessBoundary.sourceExistingFile -or
        $accessBoundary.probes.rootCreate.path -cne $childConfiguration.accessBoundary.rootCreatePath -or
        $accessBoundary.probes.packageExisting.path -cne $childConfiguration.accessBoundary.packageExistingFile -or
        $accessBoundary.probes.packageCreate.path -cne $childConfiguration.accessBoundary.packageCreatePath -or
        $accessBoundary.probes.helperExisting.path -cne $childConfiguration.accessBoundary.helperExistingFile -or
        $accessBoundary.probes.helperCreate.path -cne $childConfiguration.accessBoundary.helperCreatePath -or
        $accessBoundary.probes.wrapperAllowed.path -cne $childConfiguration.accessBoundary.wrapperAllowedPath -or
        $accessBoundary.probes.lifecycleAllowed.path -cne $childConfiguration.accessBoundary.lifecycleAllowedPath) {
        throw 'Child access-boundary evidence does not cover the configured source and evidence targets.'
    }
    if (-not [bool]$accessBoundary.passed -or
        -not [bool]$accessBoundary.probes.sourceExisting.writeOpenDenied -or
        -not [bool]$accessBoundary.probes.rootCreate.createNewDenied -or
        -not [bool]$accessBoundary.probes.packageExisting.writeOpenDenied -or
        -not [bool]$accessBoundary.probes.packageCreate.createNewDenied -or
        -not [bool]$accessBoundary.probes.helperExisting.writeOpenDenied -or
        -not [bool]$accessBoundary.probes.helperCreate.createNewDenied -or
        -not [bool]$accessBoundary.probes.wrapperAllowed.createSucceeded -or
        -not [bool]$accessBoundary.probes.wrapperAllowed.writeOpenSucceeded -or
        -not [bool]$accessBoundary.probes.wrapperAllowed.deleteSucceeded -or
        -not [bool]$accessBoundary.probes.lifecycleAllowed.createSucceeded -or
        -not [bool]$accessBoundary.probes.lifecycleAllowed.writeOpenSucceeded -or
        -not [bool]$accessBoundary.probes.lifecycleAllowed.deleteSucceeded) {
        throw 'Child effective access-boundary probes do not prove the expected deny/allow split.'
    }
    if ($childToken.identity.expectedSid -cne $UserSid.Value -or $childToken.identity.sid -cne $UserSid.Value) {
        throw 'Child token evidence does not match the temporary account SID.'
    }
    if (-not [bool]$childToken.identity.standardUserProven) { throw 'Child token evidence does not prove the non-administrator standard-user boundary.' }
    if (-not [bool]$childCompletion.lifecycleInvoked) { throw 'Child completion evidence does not prove that the lifecycle script was invoked.' }
    if (-not [bool]$childCompletion.lifecycleStateObserved -or -not [bool]$childCompletion.lifecyclePassedStateObserved -or -not [bool]$childCompletion.lifecycleRunOutcomePassed) {
        throw 'Child completion evidence does not link a parseable passing lifecycle state and run outcome.'
    }
    $lifecycleRuns = @($childCompletion.lifecycleEvidenceRuns)
    if ($lifecycleRuns.Count -ne 1) { throw "Child completion evidence reports $($lifecycleRuns.Count) new lifecycle runs; exactly one is required." }
    $ValidatedLifecycleEvidenceRun = [System.IO.Path]::GetFullPath([string]$lifecycleRuns[0])
    $lifecycleEvidenceRoot = [System.IO.Path]::GetFullPath((Join-Path $ProjectRoot '.tools\evidence\windows-msi-lifecycle'))
    if (-not $ValidatedLifecycleEvidenceRun.StartsWith($lifecycleEvidenceRoot + [System.IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Child completion evidence points outside the lifecycle evidence root.'
    }
    $validatedRunJsonPath = Join-Path $ValidatedLifecycleEvidenceRun 'run.json'
    $validatedBootstrapPath = Join-Path $ValidatedLifecycleEvidenceRun 'bootstrap.json'
    if (-not (Test-Path -LiteralPath $validatedRunJsonPath -PathType Leaf) -or -not (Test-Path -LiteralPath $validatedBootstrapPath -PathType Leaf)) {
        throw 'The exact child lifecycle run is missing run.json or bootstrap.json.'
    }
    $validatedRun = Get-Content -Raw -LiteralPath $validatedRunJsonPath | ConvertFrom-Json
    $validatedBootstrap = Get-Content -Raw -LiteralPath $validatedBootstrapPath | ConvertFrom-Json
    if ($validatedRun.outcome -cne 'passed' -or [bool]$validatedRun.msiClientTimedOut -or -not [string]::IsNullOrWhiteSpace([string]$validatedRun.cleanupSuppressedReason)) {
        throw 'The exact lifecycle run does not report a clean passing outcome.'
    }
    if (-not [bool]$validatedBootstrap.lifecyclePassed -or [bool]$validatedBootstrap.msiClientTimedOut -or -not [string]::IsNullOrWhiteSpace([string]$validatedBootstrap.cleanupSuppressedReason)) {
        throw 'The exact lifecycle bootstrap does not report lifecyclePassed=true with known MSI client state.'
    }
    $ValidatedLifecycleRunJsonSHA256 = (Get-FileHash -LiteralPath $validatedRunJsonPath -Algorithm SHA256).Hash
    $ValidatedLifecycleBootstrapSHA256 = (Get-FileHash -LiteralPath $validatedBootstrapPath -Algorithm SHA256).Hash
    if (-not [bool]$childCompletion.passed) { throw 'Child completion evidence does not report a passing lifecycle.' }
    if ($null -ne $childCompletion.failure) { throw 'Child completion evidence contains a failure record.' }
    if ([bool]$childCompletion.msiClientTimedOutOrServiceUnknown) {
        $PreserveDisposableState = $true
        $PreservationReasons += 'Child completion evidence reports MSI timeout or unknown Windows Installer service state.'
        throw 'Child completion evidence reports MSI timeout or unknown Windows Installer service state.'
    }
    if ($ChildExitCode -ne 0) { throw "Standard-user MSI lifecycle failed with child exit code $ChildExitCode." }
    $ChildEvidenceValidated = $true
    Write-OrchestratorEvidence 'child-passed'
}
catch {
    $Failure = $_
    if ($null -ne $Process -and -not $ChildEvidenceValidated) {
        $PreserveDisposableState = $true
        if ($PreservationReasons.Count -eq 0) {
            $PreservationReasons += 'The credentialed child did not reach a fully validated success boundary.'
        }
    }
    try { Write-OrchestratorEvidence 'failed' } catch { $CleanupErrors += "write failure evidence: $($_.Exception.Message)" }
}
finally {
    if ($null -ne $Process -and -not $Process.HasExited) {
        try {
            if ($ProcessAssignedToJob) { [InlaidStandardUserJob]::Terminate($JobHandle, 126) }
            else { $Process.Kill() }
        }
        catch { $CleanupErrors += "terminate child process tree: $($_.Exception.Message)" }
        try {
            if (-not $Process.WaitForExit(30000)) { throw 'The child did not exit within 30 seconds of termination.' }
        }
        catch { $CleanupErrors += "wait for terminated child: $($_.Exception.Message)" }
    }
    if ($JobHandle -ne [IntPtr]::Zero) {
        try { if (-not [InlaidStandardUserJob]::CloseHandle($JobHandle)) { throw 'CloseHandle returned false.' } }
        catch { $CleanupErrors += "close child job: $($_.Exception.Message)" }
        $JobHandle = [IntPtr]::Zero
    }
    if ($null -ne $Process) { $Process.Dispose() }
    if ($InstallerPolicyCaptured) {
        $policyRestoreErrors = @()
        if ($InstallerPolicyAppliedNames.Count -ne 0) {
            $InstallerPolicyRestorationAttempted = $true
            foreach ($name in $InstallerPolicyAppliedNames) {
                try {
                    $original = @($InstallerPolicyBefore | Where-Object { $_.name -ceq $name })
                    if ($original.Count -ne 1 -or -not [bool]$original[0].present -or $original[0].kind -cne 'DWord') {
                        if ($original.Count -eq 1 -and -not [bool]$original[0].present) {
                            Remove-MachineInstallerPolicyValue $name
                            continue
                        }
                        throw "Original installer policy evidence is not restorable for $name."
                    }
                    Set-MachineInstallerPolicyDword $name ([uint32]$original[0].value)
                }
                catch { $policyRestoreErrors += "$name`: $($_.Exception.Message)" }
            }
        }
        try {
            $InstallerPolicyAfter = @($InstallerPolicyNames | ForEach-Object { Get-MachineInstallerPolicyValue $_ })
        }
        catch { $policyRestoreErrors += "capture final state: $($_.Exception.Message)" }
        if ($InstallerPolicyAfter.Count -ne $InstallerPolicyNames.Count -or
            -not (Test-InstallerPolicyValueSnapshotsEqual $InstallerPolicyBefore $InstallerPolicyAfter)) {
            $policyRestoreErrors += 'final policy values do not exactly match the captured original values or absence'
        }
        try {
            $InstallerPolicyKeyAfter = Get-MachineInstallerPolicyKeyInventory
            if ($InstallerPolicyKeyCreated -and
                (-not [bool]$InstallerPolicyKeyAfter.present -or
                 @($InstallerPolicyKeyAfter.valueNames).Count -ne 0 -or
                 @($InstallerPolicyKeyAfter.subKeyNames).Count -ne 0)) {
                $policyRestoreErrors += 'wrapper-created Installer policy key is not present and empty after value restoration'
            }
        }
        catch { $policyRestoreErrors += "capture final policy-key inventory: $($_.Exception.Message)" }
        $InstallerPolicyRestored = $policyRestoreErrors.Count -eq 0
        if (-not $InstallerPolicyRestored) {
            $CleanupErrors += "restore machine Windows Installer policy values safely: $($policyRestoreErrors -join '; ')"
        }
    }
    if (-not $PreserveDisposableState) {
        Remove-Item -LiteralPath $GatePath -Force -ErrorAction SilentlyContinue
    }

    if (-not $PreserveDisposableState -and $null -ne $OriginalProjectAcl -and $null -ne $OriginalEvidenceAcl -and $null -ne $OriginalLifecycleEvidenceAcl) {
        try {
            Set-Acl -LiteralPath $ProjectRoot -AclObject $OriginalProjectAcl
            Set-Acl -LiteralPath $EvidenceRoot -AclObject $OriginalEvidenceAcl
            Set-Acl -LiteralPath $LifecycleEvidenceRoot -AclObject $OriginalLifecycleEvidenceAcl
            $restoredProjectSddl = (Get-Acl -LiteralPath $ProjectRoot).GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access)
            $restoredEvidenceSddl = (Get-Acl -LiteralPath $EvidenceRoot).GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access)
            $restoredLifecycleEvidenceSddl = (Get-Acl -LiteralPath $LifecycleEvidenceRoot).GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access)
            if ($restoredProjectSddl -cne $OriginalProjectAccessSddl) { throw 'Project-root access ACL did not restore exactly.' }
            if ($restoredEvidenceSddl -cne $OriginalEvidenceAccessSddl) { throw 'Wrapper evidence-root access ACL did not restore exactly.' }
            if ($restoredLifecycleEvidenceSddl -cne $OriginalLifecycleEvidenceAccessSddl) { throw 'Lifecycle evidence-root access ACL did not restore exactly.' }
        }
        catch {
            $CleanupErrors += "restore repository ACLs exactly: $($_.Exception.Message)"
            $PreserveDisposableState = $true
            $PreservationReasons += 'Exact repository ACL restoration could not be proven; the temporary identity is retained on the disposable VM.'
        }
    }

    if (-not $PreserveDisposableState -and $null -ne $UserSid) {
        try {
            $profileDeadline = [DateTime]::UtcNow.AddSeconds(20)
            do {
                $profile = Get-CimInstance -ClassName Win32_UserProfile -Filter "SID = '$($UserSid.Value)'" -ErrorAction SilentlyContinue
                if ($null -eq $profile -or -not $profile.Loaded) { break }
                Start-Sleep -Milliseconds 250
            } while ([DateTime]::UtcNow -lt $profileDeadline)
            if ($null -ne $profile -and $profile.Loaded) { throw 'Temporary user profile remained loaded after the contained process tree ended.' }
            if ($null -ne $profile) { Remove-CimInstance -InputObject $profile -ErrorAction Stop }
        }
        catch {
            $CleanupErrors += "remove temporary user profile: $($_.Exception.Message)"
            $PreserveDisposableState = $true
            $PreservationReasons += 'Temporary profile removal could not be proven; the local account is retained on the disposable VM.'
        }
    }
    if (-not $PreserveDisposableState -and $null -ne $User) {
        try { Remove-LocalUser -SID $UserSid }
        catch { $CleanupErrors += "remove temporary local user: $($_.Exception.Message)" }
    }
    if ($null -ne $Password) { $Password.Dispose() }

    try {
        [ordered]@{
            schema = 1
            completedUtc = [DateTime]::UtcNow.ToString('o')
            childTimedOut = $TimedOut
            childExitCode = $ChildExitCode
            preserveDisposableState = $PreserveDisposableState
            preservationReasons = @($PreservationReasons)
            projectAccessSddlBefore = $OriginalProjectAccessSddl
            projectAccessSddlAfter = if ($null -eq $OriginalProjectAcl) { $null } else { (Get-Acl -LiteralPath $ProjectRoot).GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access) }
            evidenceAccessSddlBefore = $OriginalEvidenceAccessSddl
            evidenceAccessSddlAfter = if ($null -eq $OriginalEvidenceAcl) { $null } else { (Get-Acl -LiteralPath $EvidenceRoot).GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access) }
            lifecycleEvidenceAccessSddlBefore = $OriginalLifecycleEvidenceAccessSddl
            lifecycleEvidenceAccessSddlAfter = if ($null -eq $OriginalLifecycleEvidenceAcl) { $null } else { (Get-Acl -LiteralPath $LifecycleEvidenceRoot).GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Access) }
            machineInstallerPolicy = [ordered]@{
                registryView = 'Registry64'
                subKey = "HKLM\$InstallerPolicySubKey"
                explicitOverrideAuthorization = $AcceptMachinePolicyOverride.IsPresent
                captured = $InstallerPolicyCaptured
                mutationRequired = $InstallerPolicyMutationRequired
                overrideApplied = $InstallerPolicyOverrideApplied
                changedNames = @($InstallerPolicyChangedNames)
                appliedNames = @($InstallerPolicyAppliedNames)
                keyCreated = $InstallerPolicyKeyCreated
                keyAfter = $InstallerPolicyKeyAfter
                verifiedEmptyCreatedKeyRetainedUntilVmDestruction = [bool]($InstallerPolicyKeyCreated -and $InstallerPolicyRestored)
                restorationAttempted = $InstallerPolicyRestorationAttempted
                valuesRestored = $InstallerPolicyRestored
                before = @($InstallerPolicyBefore)
                effective = @($InstallerPolicyEffective)
                after = @($InstallerPolicyAfter)
            }
            accountPresentAfterCleanup = if ($null -eq $UserSid) { $null } else { $null -ne (Get-LocalUser -SID $UserSid -ErrorAction SilentlyContinue) }
            profilePresentAfterCleanup = if ($null -eq $UserSid) { $null } else { $null -ne (Get-CimInstance -ClassName Win32_UserProfile -Filter "SID = '$($UserSid.Value)'" -ErrorAction SilentlyContinue) }
            cleanupErrors = @($CleanupErrors)
        } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $CleanupPath -Encoding utf8NoBOM
    }
    catch { $CleanupErrors += "write cleanup evidence: $($_.Exception.Message)" }
}

if (Test-Path -LiteralPath $StandardOutputPath -PathType Leaf) {
    Get-Content -LiteralPath $StandardOutputPath | Write-Host
}
if (Test-Path -LiteralPath $StandardErrorPath -PathType Leaf) {
    Get-Content -LiteralPath $StandardErrorPath | Write-Warning
}
if ($CleanupErrors.Count -ne 0) {
    $cleanupMessage = $CleanupErrors -join '; '
    if ($null -ne $Failure) { throw "$($Failure.Exception.Message) Cleanup also failed: $cleanupMessage" }
    throw "Standard-user MSI lifecycle cleanup failed: $cleanupMessage"
}
if ($PreserveDisposableState -and $null -eq $Failure) {
    throw "Disposable account, profile, and ACL state was retained: $($PreservationReasons -join '; ')"
}
if ($null -ne $Failure) { $PSCmdlet.ThrowTerminatingError($Failure) }

Write-Host 'Windows MSI lifecycle passed under a disposable, non-administrator local user.' -ForegroundColor Green
Write-Host "Retained standard-user wrapper evidence: $EvidenceDirectory"
