Set-StrictMode -Version Latest

function Resolve-InlaidPayload {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][ValidateSet('windows')][string]$Platform,
        [Parameter(Mandatory)][ValidateSet('msi', 'portable')][string]$Profile,
        [Parameter(Mandatory)][string]$Executable
    )

    $manifestPath = Join-Path $ProjectRoot 'packaging\payload.json'
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        throw "Release payload manifest was not found: $manifestPath"
    }
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.schema -ne 3) {
        throw "Unsupported release payload manifest schema: $($manifest.schema)"
    }
    $platformNode = $manifest.channels.$Platform
    if ($null -eq $platformNode) { throw "Payload channel platform is not defined: $Platform" }
    $channel = $platformNode.$Profile
    if ($null -eq $channel) { throw "Payload channel is not implemented: $Platform/$Profile" }
    $payloadName = [string]$channel.payload
    if ([string]::IsNullOrWhiteSpace($payloadName)) { throw "Payload channel has no common payload: $Platform/$Profile" }
    $commonPayload = @($manifest.logicalPayloads.$payloadName)
    if ($commonPayload.Count -eq 0) { throw "Payload channel references an undefined or empty common payload: $payloadName" }
    $entries = @($commonPayload + @($channel.additions))

    $seenRoles = @{}
    $seenDestinations = @{}
    $resolved = foreach ($entry in $entries) {
        $role = [string]$entry.role
        $destination = ([string]$entry.destination).Replace('/', '\')
        if ([string]::IsNullOrWhiteSpace($role) -or [string]::IsNullOrWhiteSpace($destination)) {
            throw 'Release payload entries require role and destination.'
        }
        if ($seenRoles.ContainsKey($role)) { throw "Duplicate payload role in ${Platform}/${Profile}: $role" }
        $seenRoles[$role] = $true
        if ($seenDestinations.ContainsKey($destination)) { throw "Duplicate payload destination in ${Platform}/${Profile}: $destination" }
        $seenDestinations[$destination] = $true

        if ([System.IO.Path]::IsPathRooted($destination) -or $destination -match '(^|\\)\.\.(\\|$)') {
            throw "Payload destination must be a relative non-traversing path: $destination"
        }
        $input = $manifest.logicalInputs.$role
        if ($null -eq $input -or [string]::IsNullOrWhiteSpace([string]$input.source)) {
            throw "Payload role has no logical input: $role"
        }
        $sourceToken = [string]$input.source
        $source = switch ($sourceToken) {
            '@executable' { $Executable }
            '@generated-portable-manifest' { $null }
            default { Join-Path $ProjectRoot $sourceToken }
        }
        if ($null -ne $source -and -not (Test-Path -LiteralPath $source -PathType Leaf)) {
            throw "Release file was not found for role ${role}: $source"
        }
        [pscustomobject]@{ Role = $role; Source = $source; SourceToken = $sourceToken; Destination = $destination }
    }
    return @($resolved)
}
