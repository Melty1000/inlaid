Set-StrictMode -Version Latest

function Assert-InlaidAmd64Pe32Plus {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Description
    )

    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 0x100 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) {
        throw "$Description is not a PE image: $Path"
    }

    $peOffset = [long][BitConverter]::ToInt32($bytes, 0x3c)
    if ($peOffset -lt 0x40 -or $peOffset + 26 -gt $bytes.Length -or
        $bytes[$peOffset] -ne 0x50 -or $bytes[$peOffset + 1] -ne 0x45 -or
        $bytes[$peOffset + 2] -ne 0 -or $bytes[$peOffset + 3] -ne 0) {
        throw "$Description has an invalid PE header: $Path"
    }

    $machine = [BitConverter]::ToUInt16($bytes, $peOffset + 4)
    $optionalMagic = [BitConverter]::ToUInt16($bytes, $peOffset + 24)
    if ($machine -ne 0x8664 -or $optionalMagic -ne 0x20b) {
        throw ("$Description must be an amd64 PE32+ image; machine=0x{0:x4} optional=0x{1:x4}: {2}" -f $machine, $optionalMagic, $Path)
    }

    return [ordered]@{
        machine = 'amd64'
        machineCode = '0x8664'
        format = 'PE32+'
        length = $bytes.Length
        sha256 = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    }
}
