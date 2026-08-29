[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Package,
    [Parameter(Mandatory)][string]$PortableRoot,
    [ValidateRange(0, 10000)][int]$InjectFailureAfter = 0
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if ($null -eq ('InlaidPortableNativeMethods' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class InlaidPortableNativeMethods {
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static extern bool MoveFileEx(string existingPath, string newPath, int flags);
}
'@
}

function Move-FileAtomic([string]$Source, [string]$Destination) {
    if (Test-Path -LiteralPath $Destination) {
        [void](Get-DirectFile $Destination 'Atomic replacement destination')
        $replaceExisting = 0x1
        $writeThrough = 0x8
        if (-not [InlaidPortableNativeMethods]::MoveFileEx($Source, $Destination, $replaceExisting -bor $writeThrough)) {
            $code = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
            throw [ComponentModel.Win32Exception]::new($code, "Atomic replacement failed: $Destination")
        }
        return
    }
    [System.IO.File]::Move($Source, $Destination)
}

function Get-DirectFile([string]$Path, [string]$Description) {
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if ($item.PSIsContainer -or (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0)) {
        throw "$Description must be a direct regular file: $Path"
    }
    return $item
}

function Get-DirectDirectory([string]$Path, [string]$Description) {
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if (-not $item.PSIsContainer -or (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0)) {
        throw "$Description must be a direct directory: $Path"
    }
    return $item
}

function Resolve-SafeRelativePath([string]$Root, [string]$Relative, [string]$Description) {
    $relative = ([string]$Relative).Replace('/', '\')
    $segments = @($relative -split '\\')
    if ([string]::IsNullOrWhiteSpace($relative) -or [System.IO.Path]::IsPathRooted($relative) -or
        $relative.Contains(':') -or @($segments | Where-Object {
            [string]::IsNullOrEmpty($_) -or $_ -eq '.' -or $_ -eq '..' -or $_.EndsWith(' ') -or $_.EndsWith('.')
        }).Count -ne 0) {
        throw "$Description contains an unsafe relative path: $relative"
    }
    $rootFull = [System.IO.Path]::GetFullPath($Root)
    $rootPrefix = $rootFull.TrimEnd('\') + '\'
    $full = [System.IO.Path]::GetFullPath((Join-Path $rootFull $relative))
    if (-not $full.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Description escaped its root: $relative"
    }
    $canonical = $full.Substring($rootPrefix.Length)
    if ($canonical -cne $relative) { throw "$Description is not canonical: $relative" }
    return [pscustomobject]@{ Path = $canonical; FullPath = $full }
}

function Assert-DirectParentChain([string]$Root, [string]$FullPath, [string]$Description) {
    $rootFull = (Get-DirectDirectory $Root "$Description root").FullName
    $rootPrefix = $rootFull.TrimEnd('\') + '\'
    $parent = Split-Path -Parent $FullPath
    if ($parent -ieq $rootFull.TrimEnd('\')) { return }
    if (-not $parent.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Description parent escaped its root"
    }
    $relativeParent = $parent.Substring($rootPrefix.Length)
    $current = $rootFull
    foreach ($segment in @($relativeParent -split '\\')) {
        $current = Join-Path $current $segment
        if (Test-Path -LiteralPath $current) {
            [void](Get-DirectDirectory $current "$Description parent")
        }
    }
}

function Write-TransactionState([string]$TransactionRoot, [object]$State) {
    $statePath = Join-Path $TransactionRoot 'state.json'
    $partial = Join-Path $TransactionRoot 'state.partial'
    $State | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $partial -Encoding utf8
    Move-FileAtomic $partial $statePath
}

function Assert-ReleaseOwnedPortablePath([string]$Relative, [string]$Description) {
    $path = $Relative.Replace('/', '\')
    $reservedFile = $path -ieq 'inlaid-settings.json' -or $path -ieq 'webcam-settings.json'
    $reservedTree = $path -imatch '^(recordings|snapshots|support-reports|\.tools)(\\|$)'
    $customFilter = $path -imatch '^filters(\\|$)' -and $path -ine 'filters\README.md'
    if ($reservedFile -or $reservedTree -or $customFilter) {
        throw "$Description claims a user-writable path as release payload: $Relative"
    }
}

$portableCurrentPayloadRoleContract = @(
    [pscustomobject]@{ Role = 'application'; Destination = 'inlaid.exe' }
    [pscustomobject]@{ Role = 'readme'; Destination = 'README.md' }
    [pscustomobject]@{ Role = 'license'; Destination = 'LICENSE' }
    [pscustomobject]@{ Role = 'third-party-notices'; Destination = 'THIRD_PARTY_NOTICES.md' }
    [pscustomobject]@{ Role = 'filter-documentation'; Destination = 'docs\FILTERS.md' }
    [pscustomobject]@{ Role = 'filter-directory-readme'; Destination = 'filters\README.md' }
    [pscustomobject]@{ Role = 'portable-updater'; Destination = 'update-portable.ps1' }
)
$portablePriorPayloadRoleContract = @(
    $portableCurrentPayloadRoleContract
    # CHANGELOG.md was release-owned by the legacy ZIP and is retained here as
    # the one explicit retired pair used to prove manifest-scoped removal.
    [pscustomobject]@{ Role = 'legacy-changelog'; Destination = 'CHANGELOG.md' }
)

function Resolve-ManifestFiles(
    [object]$Manifest,
    [string]$Root,
    [string]$Description,
    [ValidateSet('prior', 'target')][string]$ContractKind,
    [switch]$RequireFiles
) {
    if ($Manifest.schema -ne 2 -or $Manifest.layout -ne 'portable' -or [string]::IsNullOrWhiteSpace([string]$Manifest.version)) {
        throw "$Description is not a supported Inlaid portable manifest."
    }
    $roleContract = if ($ContractKind -eq 'target') {
        @($portableCurrentPayloadRoleContract)
    }
    else { @($portablePriorPayloadRoleContract) }
    $seen = @{}
    $seenRoles = @{}
    $resolved = foreach ($entry in @($Manifest.files)) {
        $safe = Resolve-SafeRelativePath $Root ([string]$entry.path) $Description
        $relative = $safe.Path
        $role = [string]$entry.role
        $digest = ([string]$entry.sha256).ToLowerInvariant()
        if ($relative -ieq 'inlaid-portable.json') {
            throw "$Description contains an unsafe payload path: $relative"
        }
        Assert-ReleaseOwnedPortablePath $relative $Description
        $allowedPair = @($roleContract | Where-Object {
            $_.Role -ceq $role -and $_.Destination -ceq $relative
        })
        if ($allowedPair.Count -ne 1) {
            throw "$Description contains a role/path pair outside the fixed portable payload contract: $role -> $relative"
        }
        if ($digest -notmatch '^[0-9a-f]{64}$') { throw "$Description contains an invalid SHA-256 for $relative" }
        if ($seenRoles.ContainsKey($role)) { throw "$Description contains a duplicate payload role: $role" }
        if ($seen.ContainsKey($relative)) { throw "$Description contains a duplicate payload path: $relative" }
        $seenRoles[$role] = $true
        $seen[$relative] = $true
        $full = $safe.FullPath
        Assert-DirectParentChain $Root $full "$Description payload"
        if ($RequireFiles) {
            [void](Get-DirectFile $full "$Description payload")
            $actual = (Get-FileHash -LiteralPath $full -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($actual -cne $digest) { throw "$Description payload hash mismatch for $relative" }
        }
        [pscustomobject]@{ Path = $relative; SHA256 = $digest; FullPath = $full }
    }
    if (@($resolved).Count -eq 0) { throw "$Description has no release-owned files." }
    if ($ContractKind -eq 'target') {
        if (@($resolved).Count -ne @($portableCurrentPayloadRoleContract).Count) {
            throw "$Description does not contain the exact complete current portable payload role/path set."
        }
        foreach ($required in $portableCurrentPayloadRoleContract) {
            if (-not $seenRoles.ContainsKey($required.Role) -or -not $seen.ContainsKey($required.Destination)) {
                throw "$Description is missing the required target payload pair: $($required.Role) -> $($required.Destination)"
            }
        }
    }
    return @($resolved)
}

function Copy-Atomic([string]$Source, [string]$Destination) {
    $directory = Split-Path -Parent $Destination
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    $temporary = Join-Path $directory ('.inlaid-update-' + [Guid]::NewGuid().ToString('N') + '.partial')
    try {
        Copy-Item -LiteralPath $Source -Destination $temporary
        if (Test-Path -LiteralPath $Destination -PathType Leaf) {
            Move-FileAtomic $temporary $Destination
        }
        else {
            [System.IO.File]::Move($temporary, $Destination)
        }
    }
    finally {
        Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue
    }
}

function Restore-InterruptedUpdate([string]$TransactionRoot, [string]$TargetRoot) {
    [void](Get-DirectDirectory $TransactionRoot 'Portable-update transaction')
    $statePath = Join-Path $TransactionRoot 'state.json'
    [void](Get-DirectFile $statePath 'Portable-update recovery state')
    $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
    if ($state.schema -ne 2 -or ([string]$state.targetRoot) -cne $TargetRoot -or
        $state.status -notin @('preparing', 'prepared')) {
        throw "Portable-update recovery state is invalid: $statePath"
    }
    $priorManifestPath = Join-Path $TransactionRoot 'prior-manifest.json'
    $nextManifestPath = Join-Path $TransactionRoot 'next-manifest.json'
    [void](Get-DirectFile $priorManifestPath 'Portable-update prior manifest')
    [void](Get-DirectFile $nextManifestPath 'Portable-update next manifest')
    $priorManifestHash = (Get-FileHash -LiteralPath $priorManifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $nextManifestHash = (Get-FileHash -LiteralPath $nextManifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ([string]$state.priorManifestSHA256 -notmatch '^[0-9a-f]{64}$' -or
        [string]$state.nextManifestSHA256 -notmatch '^[0-9a-f]{64}$' -or
        $priorManifestHash -cne [string]$state.priorManifestSHA256 -or
        $nextManifestHash -cne [string]$state.nextManifestSHA256) {
        throw 'Portable-update recovery manifest hashes are invalid or inconsistent.'
    }
    $priorManifest = Get-Content -LiteralPath $priorManifestPath -Raw | ConvertFrom-Json
    $nextManifest = Get-Content -LiteralPath $nextManifestPath -Raw | ConvertFrom-Json
    $priorFiles = Resolve-ManifestFiles $priorManifest $TargetRoot 'Portable-update prior manifest' prior
    $nextFiles = Resolve-ManifestFiles $nextManifest $TargetRoot 'Portable-update next manifest' target
    $markerPath = Join-Path $TargetRoot 'inlaid-portable.json'
    [void](Get-DirectFile $markerPath 'Portable-update current marker')
    $markerHash = (Get-FileHash -LiteralPath $markerPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($markerHash -cne $priorManifestHash -and $markerHash -cne $nextManifestHash) {
        throw 'Portable-update current marker does not match either fixed transaction manifest.'
    }
    if ($state.status -eq 'preparing') {
        if ($markerHash -cne $priorManifestHash) {
            throw 'Portable-update preparation state is inconsistent with the current marker.'
        }
        Remove-Item -LiteralPath $TransactionRoot -Recurse -Force
        Write-Host 'Discarded an interrupted portable-update preparation before retrying.' -ForegroundColor Yellow
        return
    }
    $prior = @{}
    foreach ($entry in $priorFiles) { $prior[$entry.Path] = $entry }
    $next = @{}
    foreach ($entry in $nextFiles) { $next[$entry.Path] = $entry }
    $existing = @{}
    foreach ($path in @($state.oldExisting)) {
        $safe = Resolve-SafeRelativePath $TargetRoot ([string]$path) 'Portable-update recovery state'
        if ($existing.ContainsKey($safe.Path)) { throw "Portable-update recovery state has a duplicate old path: $($safe.Path)" }
        if (-not $prior.ContainsKey($safe.Path)) { throw "Portable-update recovery state has an unowned backup path: $($safe.Path)" }
        $existing[$safe.Path] = $true
    }
    $backupRoot = Join-Path $TransactionRoot 'backup'
    if ($existing.Count -gt 0) { [void](Get-DirectDirectory $backupRoot 'Portable-update backup root') }
    foreach ($relative in @($existing.Keys)) {
        $backup = (Resolve-SafeRelativePath $backupRoot $relative 'Portable-update backup').FullPath
        Assert-DirectParentChain $backupRoot $backup 'Portable-update backup'
        [void](Get-DirectFile $backup 'Portable-update backup')
        $backupHash = (Get-FileHash -LiteralPath $backup -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($backupHash -cne $prior[$relative].SHA256) {
            throw "Portable-update backup hash mismatch for $relative"
        }
    }

    $actions = @()
    $affected = @(@($prior.Keys) + @($next.Keys) | Sort-Object -Unique)
    foreach ($relative in $affected) {
        $destination = (Resolve-SafeRelativePath $TargetRoot $relative 'Portable-update recovery destination').FullPath
        Assert-DirectParentChain $TargetRoot $destination 'Portable-update recovery destination'
        $currentHash = ''
        if (Test-Path -LiteralPath $destination) {
            [void](Get-DirectFile $destination 'Interrupted portable-update payload')
            $currentHash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant()
        }
        if ($existing.ContainsKey([string]$relative)) {
            $allowed = @($prior[$relative].SHA256)
            if ($next.ContainsKey($relative)) { $allowed += $next[$relative].SHA256 }
            if ($currentHash -ne '' -and $currentHash -notin $allowed) {
                throw "Interrupted portable-update payload changed after interruption; preserving it: $relative"
            }
            $backup = (Resolve-SafeRelativePath $backupRoot $relative 'Portable-update backup').FullPath
            $actions += [pscustomobject]@{ Kind = 'restore'; Source = $backup; Destination = $destination; Path = $relative }
        }
        elseif ($next.ContainsKey($relative)) {
            if ($currentHash -ne '' -and $currentHash -cne $next[$relative].SHA256) {
                throw "Interrupted portable-update introduced path is no longer the staged payload; preserving it: $relative"
            }
            if ($currentHash -ne '') {
                $actions += [pscustomobject]@{ Kind = 'remove'; Source = ''; Destination = $destination; Path = $relative }
            }
        }
        elseif ($currentHash -ne '') {
            throw "A previously absent obsolete payload path appeared during interruption; preserving it: $relative"
        }
    }
    foreach ($action in $actions) {
        if ($action.Kind -eq 'restore') { Copy-Atomic $action.Source $action.Destination }
        else { Remove-Item -LiteralPath $action.Destination -Force }
    }
    Copy-Atomic $priorManifestPath $markerPath
    Remove-Item -LiteralPath $TransactionRoot -Recurse -Force
    Write-Host 'Recovered the interrupted portable update before retrying.' -ForegroundColor Yellow
}

$portable = (Get-DirectDirectory $PortableRoot 'Portable root').FullName
$packageItem = Get-DirectFile $Package 'Portable package'
$markerPath = Join-Path $portable 'inlaid-portable.json'
[void](Get-DirectFile $markerPath 'Portable marker')
$parent = Split-Path -Parent $portable
$leaf = Split-Path -Leaf $portable
$transactionRoot = Join-Path $parent ('.' + $leaf + '.inlaid-update')
if (Test-Path -LiteralPath $transactionRoot) {
    Restore-InterruptedUpdate $transactionRoot $portable
}
$oldManifest = Get-Content -LiteralPath $markerPath -Raw | ConvertFrom-Json
$oldFiles = Resolve-ManifestFiles $oldManifest $portable 'Installed portable manifest' prior

$temporaryBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$extractRoot = Join-Path $temporaryBase ('inlaid-portable-package-' + [Guid]::NewGuid().ToString('N'))
$createdTransaction = $false
try {
    Expand-Archive -LiteralPath $packageItem.FullName -DestinationPath $extractRoot
    $packageRoots = @(Get-ChildItem -LiteralPath $extractRoot -Directory)
    if ($packageRoots.Count -ne 1) { throw 'Portable package must contain exactly one top-level directory.' }
    $newRoot = $packageRoots[0].FullName
    $newMarkerPath = Join-Path $newRoot 'inlaid-portable.json'
    [void](Get-DirectFile $newMarkerPath 'New portable marker')
    $newManifest = Get-Content -LiteralPath $newMarkerPath -Raw | ConvertFrom-Json
    $newFiles = Resolve-ManifestFiles $newManifest $newRoot 'New portable manifest' target -RequireFiles

    $oldByPath = @{}
    foreach ($entry in $oldFiles) { $oldByPath[$entry.Path] = $entry }
    $newByPath = @{}
    foreach ($entry in $newFiles) { $newByPath[$entry.Path] = $entry }
    foreach ($entry in $oldFiles) {
        if (Test-Path -LiteralPath $entry.FullPath) {
            [void](Get-DirectFile $entry.FullPath 'Existing release-owned payload')
            $actual = (Get-FileHash -LiteralPath $entry.FullPath -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($actual -cne $entry.SHA256) { throw "Existing release-owned file was modified; preserving it and stopping: $($entry.Path)" }
        }
    }
    foreach ($entry in $newFiles) {
        $destination = (Resolve-SafeRelativePath $portable $entry.Path 'New portable destination').FullPath
        Assert-DirectParentChain $portable $destination 'New portable destination'
        if (-not $oldByPath.ContainsKey($entry.Path) -and (Test-Path -LiteralPath $destination)) {
            throw "New release payload collides with a user-owned path: $($entry.Path)"
        }
    }

    New-Item -ItemType Directory -Path $transactionRoot | Out-Null
    $createdTransaction = $true
    $priorManifestPath = Join-Path $transactionRoot 'prior-manifest.json'
    $nextManifestPath = Join-Path $transactionRoot 'next-manifest.json'
    Copy-Item -LiteralPath $markerPath -Destination $priorManifestPath
    Copy-Item -LiteralPath $newMarkerPath -Destination $nextManifestPath
    $priorManifestSHA256 = (Get-FileHash -LiteralPath $priorManifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $nextManifestSHA256 = (Get-FileHash -LiteralPath $nextManifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-TransactionState $transactionRoot ([ordered]@{
        schema = 2; status = 'preparing'; targetRoot = $portable
        priorManifestSHA256 = $priorManifestSHA256; nextManifestSHA256 = $nextManifestSHA256
    })
    $backupRoot = Join-Path $transactionRoot 'backup'
    $stagedRoot = Join-Path $transactionRoot 'staged'
    $oldExisting = @()
    foreach ($entry in $oldFiles) {
        if (Test-Path -LiteralPath $entry.FullPath -PathType Leaf) {
            $backup = Join-Path $backupRoot $entry.Path
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $backup) | Out-Null
            Copy-Item -LiteralPath $entry.FullPath -Destination $backup
            $oldExisting += $entry.Path
        }
    }
    foreach ($entry in $newFiles) {
        $staged = Join-Path $stagedRoot $entry.Path
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $staged) | Out-Null
        Copy-Item -LiteralPath $entry.FullPath -Destination $staged
    }
    $state = [ordered]@{
        schema = 2
        status = 'prepared'
        targetRoot = $portable
        priorManifestSHA256 = $priorManifestSHA256
        nextManifestSHA256 = $nextManifestSHA256
        oldExisting = @($oldExisting)
    }
    Write-TransactionState $transactionRoot $state

    $mutations = 0
    foreach ($entry in $newFiles) {
        Copy-Atomic (Join-Path $stagedRoot $entry.Path) (Join-Path $portable $entry.Path)
        $mutations++
        if ($InjectFailureAfter -gt 0 -and $mutations -eq $InjectFailureAfter) {
            throw "Injected portable-update interruption after $mutations payload mutations."
        }
    }
    foreach ($entry in $oldFiles) {
        if (-not $newByPath.ContainsKey($entry.Path) -and (Test-Path -LiteralPath $entry.FullPath)) {
            [void](Get-DirectFile $entry.FullPath 'Obsolete release-owned payload')
            Remove-Item -LiteralPath $entry.FullPath -Force
            $mutations++
            if ($InjectFailureAfter -gt 0 -and $mutations -eq $InjectFailureAfter) {
                throw "Injected portable-update interruption after $mutations payload mutations."
            }
        }
    }
    Copy-Atomic $newMarkerPath $markerPath
    [void](Get-DirectDirectory $transactionRoot 'Portable-update committed transaction')
    Remove-Item -LiteralPath $transactionRoot -Recurse -Force
    Write-Host "Portable Inlaid updated from $($oldManifest.version) to $($newManifest.version)." -ForegroundColor Green
}
catch {
    if ($createdTransaction -and (Test-Path -LiteralPath $transactionRoot) -and
        -not (Test-Path -LiteralPath (Join-Path $transactionRoot 'state.json') -PathType Leaf)) {
        [void](Get-DirectDirectory $transactionRoot 'Portable-update transaction cleanup')
        Remove-Item -LiteralPath $transactionRoot -Recurse -Force
    }
    throw
}
finally {
    if (Test-Path -LiteralPath $extractRoot) {
        $resolvedExtract = [System.IO.Path]::GetFullPath($extractRoot)
        if ($resolvedExtract.StartsWith($temporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) {
            [void](Get-DirectDirectory $resolvedExtract 'Portable package extraction cleanup')
            Remove-Item -LiteralPath $resolvedExtract -Recurse -Force
        }
    }
}
