[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ConfigurationPath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$ConfigurationPath = [System.IO.Path]::GetFullPath($ConfigurationPath)
$configurationHashAtStart = (Get-FileHash -LiteralPath $ConfigurationPath -Algorithm SHA256).Hash
$childScriptHashAtStart = (Get-FileHash -LiteralPath $PSCommandPath -Algorithm SHA256).Hash
$configuration = Get-Content -Raw -LiteralPath $ConfigurationPath | ConvertFrom-Json
$failure = $null
$passed = $false
$probeCreated = $false
$lifecycleInvoked = $false
$lifecycleEvidenceBefore = @()
$lifecycleEvidenceRuns = @()
$lifecycleStateObserved = $false
$lifecyclePassedStateObserved = $false
$lifecycleRunOutcomePassed = $false
$msiClientTimedOutOrServiceUnknown = $false
$preserveDisposableState = $false
$preservationReasons = @()
$accessBoundaryPassed = $false
$accessBoundarySHA256 = ''
$accessBoundaryPath = Join-Path $configuration.evidenceDirectory 'child-access-boundary.json'
$childStartedUtc = [DateTime]::UtcNow

function Test-ExistingWriteDenied([string]$Path) {
    $result = [ordered]@{ path = $Path; exists = Test-Path -LiteralPath $Path -PathType Leaf; writeOpenDenied = $false; error = $null }
    if (-not $result.exists) { $result.error = 'Expected existing file is missing.'; return $result }
    $stream = $null
    try {
        $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Write, [System.IO.FileShare]::ReadWrite)
    }
    catch [UnauthorizedAccessException] { $result.writeOpenDenied = $true }
    catch { $result.error = $_.Exception.Message }
    finally { if ($null -ne $stream) { $stream.Dispose() } }
    return $result
}

function Test-CreateDenied([string]$Path) {
    $result = [ordered]@{ path = $Path; absentBefore = -not (Test-Path -LiteralPath $Path); createNewDenied = $false; createdUnexpectedly = $false; error = $null }
    if (-not $result.absentBefore) { $result.error = 'CreateNew probe path already exists.'; return $result }
    $stream = $null
    try {
        $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        $result.createdUnexpectedly = $true
    }
    catch [UnauthorizedAccessException] { $result.createNewDenied = $true }
    catch { $result.error = $_.Exception.Message }
    finally { if ($null -ne $stream) { $stream.Dispose() } }
    return $result
}

function Test-AllowedCreateWriteDelete([string]$Path) {
    $result = [ordered]@{ path = $Path; absentBefore = -not (Test-Path -LiteralPath $Path); createSucceeded = $false; writeOpenSucceeded = $false; deleteSucceeded = $false; error = $null }
    if (-not $result.absentBefore) { $result.error = 'Allowed probe path already exists.'; return $result }
    $stream = $null
    try {
        $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        $stream.WriteByte(65)
        $stream.Dispose()
        $stream = $null
        $result.createSucceeded = $true
        $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        $stream.Seek(0, [System.IO.SeekOrigin]::End) | Out-Null
        $stream.WriteByte(66)
        $stream.Dispose()
        $stream = $null
        $result.writeOpenSucceeded = $true
        Remove-Item -LiteralPath $Path -Force
        $result.deleteSucceeded = -not (Test-Path -LiteralPath $Path)
    }
    catch { $result.error = $_.Exception.Message }
    finally { if ($null -ne $stream) { $stream.Dispose() } }
    return $result
}

try {
    $gateDeadline = [DateTime]::UtcNow.AddSeconds(90)
    while (-not (Test-Path -LiteralPath $configuration.gatePath -PathType Leaf)) {
        if ([DateTime]::UtcNow -ge $gateDeadline) { throw 'The parent did not release the bounded child gate.' }
        Start-Sleep -Milliseconds 100
    }

    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    $administratorEnabled = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    $identityParts = $identity.Name -split '\\', 2
    $profile = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
    $localAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    $roamingAppData = [Environment]::GetFolderPath([Environment+SpecialFolder]::ApplicationData)
    if ([string]::IsNullOrWhiteSpace($profile) -or [string]::IsNullOrWhiteSpace($localAppData) -or [string]::IsNullOrWhiteSpace($roamingAppData)) {
        throw 'The temporary account profile did not resolve its required known folders.'
    }

    $env:USERPROFILE = $profile
    $env:HOME = $profile
    $env:USERDOMAIN = $identityParts[0]
    $env:USERNAME = $identityParts[-1]
    $env:HOMEDRIVE = [System.IO.Path]::GetPathRoot($profile).TrimEnd('\')
    $env:HOMEPATH = $profile.Substring($env:HOMEDRIVE.Length)
    $env:LOCALAPPDATA = $localAppData
    $env:APPDATA = $roamingAppData
    $env:TEMP = Join-Path $localAppData 'Temp'
    $env:TMP = $env:TEMP
    foreach ($property in $configuration.toolEnvironment.PSObject.Properties) {
        [Environment]::SetEnvironmentVariable($property.Name, [string]$property.Value, [EnvironmentVariableTarget]::Process)
    }
    foreach ($property in $configuration.runnerFacts.PSObject.Properties) {
        [Environment]::SetEnvironmentVariable($property.Name, [string]$property.Value, [EnvironmentVariableTarget]::Process)
    }
    $env:DOTNET_ADD_GLOBAL_TOOLS_TO_PATH = '0'
    $env:DOTNET_CLI_HOME = Join-Path $profile '.dotnet'
    $env:NUGET_PACKAGES = Join-Path $profile '.nuget\packages'
    $env:GOPATH = Join-Path $profile 'go'
    $env:GOENV = 'off'
    $env:GOTOOLCHAIN = 'local'
    $env:GOFLAGS = ''
    Remove-Item Env:GOCACHE -ErrorAction SilentlyContinue
    Remove-Item Env:GOMODCACHE -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $env:TEMP | Out-Null
    New-Item -ItemType Directory -Force -Path $env:DOTNET_CLI_HOME | Out-Null
    New-Item -ItemType Directory -Force -Path $env:NUGET_PACKAGES | Out-Null
    New-Item -ItemType Directory -Force -Path $env:GOPATH | Out-Null
    Set-Location -LiteralPath $configuration.projectRoot

    $whoami = @(& whoami.exe /all 2>&1)
    $whoamiExitCode = $LASTEXITCODE
    $whoami | Set-Content -LiteralPath (Join-Path $configuration.evidenceDirectory 'child-whoami-all.txt') -Encoding utf8NoBOM
    $tokenGroupSids = @($identity.Groups | ForEach-Object { $_.Value } | Sort-Object)
    $administratorsSidPresentInToken = $tokenGroupSids -contains 'S-1-5-32-544'
    $usersSidPresentInToken = $tokenGroupSids -contains 'S-1-5-32-545'

    $probePath = 'Software\Inlaid\StandardUserHarness\' + $configuration.runId
    $probe = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($probePath, $true)
    if ($null -eq $probe) { throw 'Could not create the temporary HKCU profile probe.' }
    try {
        $probe.SetValue('Sid', $identity.User.Value, [Microsoft.Win32.RegistryValueKind]::String)
        if ([string]$probe.GetValue('Sid', '') -cne $identity.User.Value) { throw 'The temporary HKCU profile probe did not round-trip.' }
        $probeCreated = $true
    }
    finally { $probe.Dispose() }

    $accessResults = [ordered]@{
        sourceExisting = Test-ExistingWriteDenied ([string]$configuration.accessBoundary.sourceExistingFile)
        rootCreate = Test-CreateDenied ([string]$configuration.accessBoundary.rootCreatePath)
        packageExisting = Test-ExistingWriteDenied ([string]$configuration.accessBoundary.packageExistingFile)
        packageCreate = Test-CreateDenied ([string]$configuration.accessBoundary.packageCreatePath)
        helperExisting = Test-ExistingWriteDenied ([string]$configuration.accessBoundary.helperExistingFile)
        helperCreate = Test-CreateDenied ([string]$configuration.accessBoundary.helperCreatePath)
        wrapperAllowed = Test-AllowedCreateWriteDelete ([string]$configuration.accessBoundary.wrapperAllowedPath)
        lifecycleAllowed = Test-AllowedCreateWriteDelete ([string]$configuration.accessBoundary.lifecycleAllowedPath)
    }
    $accessBoundaryPassed = (
        $accessResults.sourceExisting.writeOpenDenied -and
        $accessResults.rootCreate.createNewDenied -and
        $accessResults.packageExisting.writeOpenDenied -and
        $accessResults.packageCreate.createNewDenied -and
        $accessResults.helperExisting.writeOpenDenied -and
        $accessResults.helperCreate.createNewDenied -and
        $accessResults.wrapperAllowed.createSucceeded -and $accessResults.wrapperAllowed.writeOpenSucceeded -and $accessResults.wrapperAllowed.deleteSucceeded -and
        $accessResults.lifecycleAllowed.createSucceeded -and $accessResults.lifecycleAllowed.writeOpenSucceeded -and $accessResults.lifecycleAllowed.deleteSucceeded
    )
    [ordered]@{
        schema = 1
        createdUtc = [DateTime]::UtcNow.ToString('o')
        runId = $configuration.runId
        sid = $identity.User.Value
        expectedSid = $configuration.expectedSid
        passed = $accessBoundaryPassed
        execution = [ordered]@{
            childScript = $PSCommandPath
            childScriptSHA256 = $childScriptHashAtStart
            configuration = $ConfigurationPath
            configurationSHA256 = $configurationHashAtStart
        }
        probes = $accessResults
    } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $accessBoundaryPath -Encoding utf8NoBOM
    $accessBoundarySHA256 = (Get-FileHash -LiteralPath $accessBoundaryPath -Algorithm SHA256).Hash

    $facts = [ordered]@{
        schema = 1
        createdUtc = [DateTime]::UtcNow.ToString('o')
        identity = [ordered]@{
            name = $identity.Name
            sid = $identity.User.Value
            expectedSid = $configuration.expectedSid
            authenticationType = $identity.AuthenticationType
            isAuthenticated = $identity.IsAuthenticated
            impersonationLevel = [string]$identity.ImpersonationLevel
            administratorRoleEnabledInCurrentToken = $administratorEnabled
            administratorsSidPresentInToken = $administratorsSidPresentInToken
            usersSidPresentInToken = $usersSidPresentInToken
            tokenGroupSids = $tokenGroupSids
            whoamiExitCode = $whoamiExitCode
            standardUserProven = ($identity.User.Value -ceq $configuration.expectedSid -and -not $administratorEnabled -and -not $administratorsSidPresentInToken -and $usersSidPresentInToken -and $whoamiExitCode -eq 0)
        }
        profile = [ordered]@{
            userProfile = $profile
            localAppData = $localAppData
            roamingAppData = $roamingAppData
            home = $env:HOME
            homeDrive = $env:HOMEDRIVE
            homePath = $env:HOMEPATH
            temp = $env:TEMP
            hkcuProbeWriteReadSucceeded = $probeCreated
        }
        execution = [ordered]@{
            childScript = $PSCommandPath
            childScriptSHA256 = $childScriptHashAtStart
            configuration = $ConfigurationPath
            configurationSHA256 = $configurationHashAtStart
            accessBoundary = $accessBoundaryPath
            accessBoundarySHA256 = $accessBoundarySHA256
        }
        runner = [ordered]@{
            runnerOS = $env:RUNNER_OS
            runnerArchitecture = $env:RUNNER_ARCH
            runnerEnvironment = $env:RUNNER_ENVIRONMENT
            runnerName = $env:RUNNER_NAME
            imageOS = $env:ImageOS
            imageVersion = $env:ImageVersion
            githubActions = $env:GITHUB_ACTIONS
            machine = $env:COMPUTERNAME
            repository = $env:GITHUB_REPOSITORY
            workflow = $env:GITHUB_WORKFLOW
            job = $env:GITHUB_JOB
            runId = $env:GITHUB_RUN_ID
            runAttempt = $env:GITHUB_RUN_ATTEMPT
            sha = $env:GITHUB_SHA
            windowsVersion = [Environment]::OSVersion.VersionString
            powershellEdition = $PSVersionTable.PSEdition
            powershellVersion = [string]$PSVersionTable.PSVersion
            currentDirectory = (Get-Location).Path
        }
    }
    $facts | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $configuration.evidenceDirectory 'child-token-and-runner.json') -Encoding utf8NoBOM

    if ($identity.User.Value -cne $configuration.expectedSid) { throw "Child SID '$($identity.User.Value)' does not match the temporary account SID." }
    if ($administratorEnabled) { throw 'The lifecycle child has the Administrators role enabled in its actual process token.' }
    if ($administratorsSidPresentInToken) { throw 'The lifecycle child actual process token contains the local Administrators SID.' }
    if (-not $usersSidPresentInToken) { throw 'The lifecycle child actual process token does not contain the local Users SID.' }
    if ($whoamiExitCode -ne 0) { throw "whoami /all failed with exit code $whoamiExitCode." }
    if (-not $probeCreated) { throw 'HKCU profile readiness was not proven.' }
    if (-not $accessBoundaryPassed) { throw 'The effective source/evidence access-boundary probes did not pass.' }

    if (Test-Path -LiteralPath $configuration.lifecycleEvidenceRoot -PathType Container) {
        $lifecycleEvidenceBefore = @(Get-ChildItem -LiteralPath $configuration.lifecycleEvidenceRoot -Directory | ForEach-Object { $_.FullName })
    }
    $lifecycleInvoked = $true
    & $configuration.lifecycleScript -Wix $configuration.wixPath -AcceptInstall
    $passed = $true
}
catch {
    $failure = $_
    Write-Error -ErrorRecord $_ -ErrorAction Continue
}
finally {
    if ($lifecycleInvoked -and (Test-Path -LiteralPath $configuration.lifecycleEvidenceRoot -PathType Container)) {
        $lifecycleEvidenceRuns = @(Get-ChildItem -LiteralPath $configuration.lifecycleEvidenceRoot -Directory | Where-Object {
            $lifecycleEvidenceBefore -notcontains $_.FullName
        } | ForEach-Object { $_.FullName })
        foreach ($run in $lifecycleEvidenceRuns) {
            foreach ($jsonPath in @((Join-Path $run 'run.json'), (Join-Path $run 'bootstrap.json')) + @(Get-ChildItem -LiteralPath (Join-Path $run 'states') -Filter '*.json' -File -ErrorAction SilentlyContinue | ForEach-Object { $_.FullName })) {
                if (-not (Test-Path -LiteralPath $jsonPath -PathType Leaf)) { continue }
                try {
                    $state = Get-Content -Raw -LiteralPath $jsonPath | ConvertFrom-Json
                    $outcomeProperty = $state.PSObject.Properties['outcome']
                    if ($null -ne $outcomeProperty -and [string]$outcomeProperty.Value -ceq 'passed') { $lifecycleRunOutcomePassed = $true }
                    $lifecyclePassedProperty = $state.PSObject.Properties['lifecyclePassed']
                    if ($null -ne $lifecyclePassedProperty -and [bool]$lifecyclePassedProperty.Value) { $lifecyclePassedStateObserved = $true }
                    $timedOutProperty = $state.PSObject.Properties['msiClientTimedOut']
                    if ($null -ne $timedOutProperty) {
                        $lifecycleStateObserved = $true
                        if ([bool]$timedOutProperty.Value) { $msiClientTimedOutOrServiceUnknown = $true }
                        $cleanupProperty = $state.PSObject.Properties['cleanupSuppressedReason']
                        if ($null -ne $cleanupProperty -and -not [string]::IsNullOrWhiteSpace([string]$cleanupProperty.Value)) { $msiClientTimedOutOrServiceUnknown = $true }
                    }
                    $msiClientProperty = $state.PSObject.Properties['msiClient']
                    if ($null -ne $msiClientProperty -and $null -ne $msiClientProperty.Value) {
                        $lifecycleStateObserved = $true
                        $clientTimedOut = $msiClientProperty.Value.PSObject.Properties['timedOut']
                        $clientSuppressed = $msiClientProperty.Value.PSObject.Properties['cleanupSuppressedReason']
                        if ($null -ne $clientTimedOut -and [bool]$clientTimedOut.Value) { $msiClientTimedOutOrServiceUnknown = $true }
                        if ($null -ne $clientSuppressed -and -not [string]::IsNullOrWhiteSpace([string]$clientSuppressed.Value)) { $msiClientTimedOutOrServiceUnknown = $true }
                    }
                }
                catch {
                    $preservationReasons += "Could not parse lifecycle state evidence '$jsonPath': $($_.Exception.Message)"
                }
            }
        }
    }
    if ($passed -and ($lifecycleEvidenceRuns.Count -ne 1 -or -not $lifecycleStateObserved -or -not $lifecyclePassedStateObserved -or -not $lifecycleRunOutcomePassed)) {
        $passed = $false
        $preserveDisposableState = $true
        $preservationReasons += 'Lifecycle execution returned without exactly one passing, parseable retained lifecycle evidence run.'
    }
    if ($msiClientTimedOutOrServiceUnknown) {
        $preserveDisposableState = $true
        $preservationReasons += 'The inner lifecycle reported an MSI client timeout or unknown Windows Installer service state.'
    }
    if ($lifecycleInvoked -and -not $passed) {
        $preserveDisposableState = $true
        $preservationReasons += 'The invoked MSI lifecycle failed; its disposable account, profile, and repository ACLs are retained for the disposable VM lifetime.'
    }
    if ($lifecycleInvoked -and -not $passed -and -not $lifecycleStateObserved) {
        $preserveDisposableState = $true
        $preservationReasons += 'No parseable lifecycle service-state evidence was observed after failure.'
    }
    if ($probeCreated) {
        try {
            if (-not $preserveDisposableState) {
                [Microsoft.Win32.Registry]::CurrentUser.DeleteSubKeyTree(('Software\Inlaid\StandardUserHarness\' + $configuration.runId), $false)
            }
        }
        catch { Write-Warning "Could not remove the temporary HKCU profile probe: $($_.Exception.Message)" }
    }
    [ordered]@{
        schema = 1
        startedUtc = $childStartedUtc.ToString('o')
        completedUtc = [DateTime]::UtcNow.ToString('o')
        passed = $passed
        lifecycleInvoked = $lifecycleInvoked
        lifecycleStateObserved = $lifecycleStateObserved
        lifecyclePassedStateObserved = $lifecyclePassedStateObserved
        lifecycleRunOutcomePassed = $lifecycleRunOutcomePassed
        lifecycleEvidenceRuns = @($lifecycleEvidenceRuns)
        msiClientTimedOutOrServiceUnknown = $msiClientTimedOutOrServiceUnknown
        preserveDisposableState = $preserveDisposableState
        preservationReasons = @($preservationReasons)
        execution = [ordered]@{
            childScript = $PSCommandPath
            childScriptSHA256 = $childScriptHashAtStart
            configuration = $ConfigurationPath
            configurationSHA256 = $configurationHashAtStart
            accessBoundary = $accessBoundaryPath
            accessBoundarySHA256 = $accessBoundarySHA256
        }
        failure = if ($null -eq $failure) { $null } else { [ordered]@{
            message = $failure.Exception.Message
            category = [string]$failure.CategoryInfo
            fullyQualifiedErrorId = $failure.FullyQualifiedErrorId
            scriptStackTrace = $failure.ScriptStackTrace
        } }
    } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $configuration.evidenceDirectory 'child-completion.json') -Encoding utf8NoBOM
}

if ($passed) { exit 0 }
exit 1
