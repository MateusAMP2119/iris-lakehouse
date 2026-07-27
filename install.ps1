# Iris installer for Windows: fetches the latest release binary for this platform.
# Windows PowerShell 5.1+ and PowerShell 7+. (UTF-8 with BOM: PS 5.1 needs the
# BOM to parse the banner's box-drawing glyphs.)
#
# Recommended:
#   irm https://install.iris-lakehouse.bymarreco.com/install.ps1 | iex
#   irm https://install.iris-lakehouse.bymarreco.com/snapshot.ps1 | iex
#
# Current (raw GitHub):
#   irm https://raw.githubusercontent.com/MateusAMP2119/iris-lakehouse/HEAD/install.ps1 | iex
#
# Knobs (environment variables; the pipe-to-iex form takes no parameters):
#   IRIS_VERSION=<tag>   release tag to install ("snapshot" -> rolling development build)
#   IRIS_BASE_URL=<url>  fetch the asset + checksums from here (local testing)
#   IRIS_DEST=<dir>      install into this directory (default ~\.iris\bin)
#   IRIS_ENGINE_SETUP=<local|remote|skip>          answer the engine-setup menu without a prompt
#   IRIS_SETUP_CATALOGS=<public|skip|url[,url...]>  answer the catalog menu without a prompt
#   NO_COLOR             plain output
#
# Errors throw (never `exit`): under `irm | iex` an exit would close the
# caller's shell. Run as a file, an uncaught throw still yields exit code 1.

$ErrorActionPreference = 'Stop'

# Windows PowerShell 5.1 defaults to TLS 1.0; GitHub requires 1.2+.
if ($PSVersionTable.PSVersion.Major -lt 6) {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
}
# Banner glyphs are multibyte; make sure they reach the console as UTF-8.
try { [Console]::OutputEncoding = [Text.Encoding]::UTF8 } catch {}

$Repo = 'MateusAMP2119/iris-lakehouse'
$Requested = 'latest'
$Base = "https://github.com/$Repo/releases/latest/download"
if ($env:IRIS_VERSION) {
    $Requested = $env:IRIS_VERSION
    $Base = "https://github.com/$Repo/releases/download/$($env:IRIS_VERSION)"
}
if ($env:IRIS_BASE_URL) {
    $Base = $env:IRIS_BASE_URL
    $Requested = "$(if ($env:IRIS_VERSION) { $env:IRIS_VERSION } else { 'latest' }) (from $Base)"
}

# Colors only on a real console, never under NO_COLOR. Legacy conhost (PS 5.1
# outside Windows Terminal) needs virtual-terminal processing switched on for
# the ANSI gradient; failure to enable it just means a plain banner.
$UseColor = (-not $env:NO_COLOR) -and (-not [Console]::IsOutputRedirected)
if ($UseColor -and $PSVersionTable.PSVersion.Major -lt 6 -and -not $env:WT_SESSION) {
    try {
        Add-Type -Namespace IrisInstall -Name Console -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)] public static extern IntPtr GetStdHandle(int nStdHandle);
[DllImport("kernel32.dll", SetLastError = true)] public static extern bool GetConsoleMode(IntPtr hConsoleHandle, out uint lpMode);
[DllImport("kernel32.dll", SetLastError = true)] public static extern bool SetConsoleMode(IntPtr hConsoleHandle, uint dwMode);
'@
        $handle = [IrisInstall.Console]::GetStdHandle(-11) # STD_OUTPUT_HANDLE
        $mode = 0
        if ([IrisInstall.Console]::GetConsoleMode($handle, [ref]$mode)) {
            # 0x4 = ENABLE_VIRTUAL_TERMINAL_PROCESSING
            $UseColor = [IrisInstall.Console]::SetConsoleMode($handle, $mode -bor 4)
        }
    } catch { $UseColor = $false }
}

# Banner: verbatim `npx oh-my-logo "IRIS LAKEHOUSE" ocean --filled` output,
# captured with its ANSI per-character ocean gradient (regenerate with that
# command and re-encode as UTF-8 base64 when the logo changes).
$BannerB64 = @(
'G1szODsyOzEwMjsxMjY7MjM0bSAbWzM4OzI7MTAyOzEyNTsyMzNt4paIG1szODsyOzEwMjsxMjU7MjMybeKWiBtbMzg7MjsxMDM7'
'MTI0OzIzMW3ilZcbWzM4OzI7MTAzOzEyMzsyMzBtIBtbMzg7MjsxMDM7MTIzOzIyOW3ilogbWzM4OzI7MTAzOzEyMjsyMjht4paI'
'G1szODsyOzEwNDsxMjE7MjI3beKWiBtbMzg7MjsxMDQ7MTIwOzIyNm3ilogbWzM4OzI7MTA0OzEyMDsyMjVt4paIG1szODsyOzEw'
'NDsxMTk7MjI0beKWiBtbMzg7MjsxMDQ7MTE4OzIyM23ilZcbWzM4OzI7MTA1OzExODsyMjJtIBtbMzg7MjsxMDU7MTE3OzIyMW0g'
'G1szODsyOzEwNTsxMTY7MjIwbeKWiBtbMzg7MjsxMDU7MTE2OzIxOW3ilogbWzM4OzI7MTA1OzExNTsyMTht4pWXG1szODsyOzEw'
'NjsxMTQ7MjE3bSAbWzM4OzI7MTA2OzExNDsyMTZt4paIG1szODsyOzEwNjsxMTM7MjE2beKWiBtbMzg7MjsxMDY7MTEyOzIxNW3i'
'logbWzM4OzI7MTA3OzExMjsyMTRt4paIG1szODsyOzEwNzsxMTE7MjEzbeKWiBtbMzg7MjsxMDc7MTEwOzIxMm3ilogbWzM4OzI7'
'MTA3OzEwOTsyMTFt4paIG1szODsyOzEwNzsxMDk7MjEwbeKVlxtbMzg7MjsxMDg7MTA4OzIwOW0gG1szODsyOzEwODsxMDc7MjA4'
'bSAbWzM4OzI7MTA4OzEwNzsyMDdtIBtbMzg7MjsxMDg7MTA2OzIwNm0gG1szODsyOzEwODsxMDU7MjA1bSAbWzM4OzI7MTA5OzEw'
'NTsyMDRt4paIG1szODsyOzEwOTsxMDQ7MjAzbeKWiBtbMzg7MjsxMDk7MTAzOzIwMm3ilZcbWzM4OzI7MTA5OzEwMzsyMDFtIBtb'
'Mzg7MjsxMTA7MTAyOzIwMG0gG1szODsyOzExMDsxMDE7MTk5bSAbWzM4OzI7MTEwOzEwMTsxOThtIBtbMzg7MjsxMTA7MTAwOzE5'
'N20gG1szODsyOzExMDs5OTsxOTZtIBtbMzg7MjsxMTE7OTg7MTk1bSAbWzM4OzI7MTExOzk4OzE5NG3ilogbWzM4OzI7MTExOzk3'
'OzE5M23ilogbWzM4OzI7MTExOzk2OzE5Mm3ilogbWzM4OzI7MTEyOzk2OzE5MW3ilogbWzM4OzI7MTEyOzk1OzE5MG3ilogbWzM4'
'OzI7MTEyOzk0OzE4OW3ilZcbWzM4OzI7MTEyOzk0OzE4OG0gG1szODsyOzExMjs5MzsxODdtIBtbMzg7MjsxMTM7OTI7MTg2beKW'
'iBtbMzg7MjsxMTM7OTI7MTg1beKWiBtbMzg7MjsxMTM7OTE7MTg0beKVlxtbMzg7MjsxMTM7OTA7MTgzbSAbWzM4OzI7MTEzOzg5'
'OzE4Mm0gG1szODsyOzExNDs4OTsxODFt4paIG1szODsyOzExNDs4ODsxODBt4paIG1szODsyOzExNDs4NzsxODBt4pWXG1szODsy'
'OzExNDs4NzsxNzltIBtbMzg7MjsxMTU7ODY7MTc4beKWiBtbMzg7MjsxMTU7ODU7MTc3beKWiBtbMzg7MjsxMTU7ODU7MTc2beKW'
'iBtbMzg7MjsxMTU7ODQ7MTc1beKWiBtbMzg7MjsxMTU7ODM7MTc0beKWiBtbMzg7MjsxMTY7ODM7MTczbeKWiBtbMzg7MjsxMTY7'
'ODI7MTcybeKWiBtbMzg7MjsxMTY7ODE7MTcxbeKVlxtbMzg7MjsxMTY7ODE7MTcwbSAbWzM4OzI7MTE2OzgwOzE2OW3ilogbWzM4'
'OzI7MTE3Ozc5OzE2OG3ilogbWzM4OzI7MTE3Ozc4OzE2N23ilZcbWzM4OzI7MTE3Ozc4OzE2Nm0gG1szODsyOzExNzs3NzsxNjVt'
'IBtbMzg7MjsxMTg7NzY7MTY0beKWiBtbMzg7MjsxMTg7NzY7MTYzbeKWiBtbMzg7MjsxMTg7NzU7MTYybeKVlxtbMzltChtbMzg7'
'MjsxMDI7MTI2OzIzNG0gG1szODsyOzEwMjsxMjU7MjMzbeKWiBtbMzg7MjsxMDI7MTI1OzIzMm3ilogbWzM4OzI7MTAzOzEyNDsy'
'MzFt4pWRG1szODsyOzEwMzsxMjM7MjMwbSAbWzM4OzI7MTAzOzEyMzsyMjlt4paIG1szODsyOzEwMzsxMjI7MjI4beKWiBtbMzg7'
'MjsxMDQ7MTIxOzIyN23ilZQbWzM4OzI7MTA0OzEyMDsyMjZt4pWQG1szODsyOzEwNDsxMjA7MjI1beKVkBtbMzg7MjsxMDQ7MTE5'
'OzIyNG3ilogbWzM4OzI7MTA0OzExODsyMjNt4paIG1szODsyOzEwNTsxMTg7MjIybeKVlxtbMzg7MjsxMDU7MTE3OzIyMW0gG1sz'
'ODsyOzEwNTsxMTY7MjIwbeKWiBtbMzg7MjsxMDU7MTE2OzIxOW3ilogbWzM4OzI7MTA1OzExNTsyMTht4pWRG1szODsyOzEwNjsx'
'MTQ7MjE3bSAbWzM4OzI7MTA2OzExNDsyMTZt4paIG1szODsyOzEwNjsxMTM7MjE2beKWiBtbMzg7MjsxMDY7MTEyOzIxNW3ilZQb'
'WzM4OzI7MTA3OzExMjsyMTRt4pWQG1szODsyOzEwNzsxMTE7MjEzbeKVkBtbMzg7MjsxMDc7MTEwOzIxMm3ilZAbWzM4OzI7MTA3'
'OzEwOTsyMTFt4pWQG1szODsyOzEwNzsxMDk7MjEwbeKVnRtbMzg7MjsxMDg7MTA4OzIwOW0gG1szODsyOzEwODsxMDc7MjA4bSAb'
'WzM4OzI7MTA4OzEwNzsyMDdtIBtbMzg7MjsxMDg7MTA2OzIwNm0gG1szODsyOzEwODsxMDU7MjA1bSAbWzM4OzI7MTA5OzEwNTsy'
'MDRt4paIG1szODsyOzEwOTsxMDQ7MjAzbeKWiBtbMzg7MjsxMDk7MTAzOzIwMm3ilZEbWzM4OzI7MTA5OzEwMzsyMDFtIBtbMzg7'
'MjsxMTA7MTAyOzIwMG0gG1szODsyOzExMDsxMDE7MTk5bSAbWzM4OzI7MTEwOzEwMTsxOThtIBtbMzg7MjsxMTA7MTAwOzE5N20g'
'G1szODsyOzExMDs5OTsxOTZtIBtbMzg7MjsxMTE7OTg7MTk1beKWiBtbMzg7MjsxMTE7OTg7MTk0beKWiBtbMzg7MjsxMTE7OTc7'
'MTkzbeKVlBtbMzg7MjsxMTE7OTY7MTkybeKVkBtbMzg7MjsxMTI7OTY7MTkxbeKVkBtbMzg7MjsxMTI7OTU7MTkwbeKWiBtbMzg7'
'MjsxMTI7OTQ7MTg5beKWiBtbMzg7MjsxMTI7OTQ7MTg4beKVlxtbMzg7MjsxMTI7OTM7MTg3bSAbWzM4OzI7MTEzOzkyOzE4Nm3i'
'logbWzM4OzI7MTEzOzkyOzE4NW3ilogbWzM4OzI7MTEzOzkxOzE4NG3ilZEbWzM4OzI7MTEzOzkwOzE4M20gG1szODsyOzExMzs4'
'OTsxODJt4paIG1szODsyOzExNDs4OTsxODFt4paIG1szODsyOzExNDs4ODsxODBt4pWUG1szODsyOzExNDs4NzsxODBt4pWdG1sz'
'ODsyOzExNDs4NzsxNzltIBtbMzg7MjsxMTU7ODY7MTc4beKWiBtbMzg7MjsxMTU7ODU7MTc3beKWiBtbMzg7MjsxMTU7ODU7MTc2'
'beKVlBtbMzg7MjsxMTU7ODQ7MTc1beKVkBtbMzg7MjsxMTU7ODM7MTc0beKVkBtbMzg7MjsxMTY7ODM7MTczbeKVkBtbMzg7Mjsx'
'MTY7ODI7MTcybeKVkBtbMzg7MjsxMTY7ODE7MTcxbeKVnRtbMzg7MjsxMTY7ODE7MTcwbSAbWzM4OzI7MTE2OzgwOzE2OW3ilogb'
'WzM4OzI7MTE3Ozc5OzE2OG3ilogbWzM4OzI7MTE3Ozc4OzE2N23ilZEbWzM4OzI7MTE3Ozc4OzE2Nm0gG1szODsyOzExNzs3Nzsx'
'NjVtIBtbMzg7MjsxMTg7NzY7MTY0beKWiBtbMzg7MjsxMTg7NzY7MTYzbeKWiBtbMzg7MjsxMTg7NzU7MTYybeKVkRtbMzltChtb'
'Mzg7MjsxMDI7MTI2OzIzNG0gG1szODsyOzEwMjsxMjU7MjMzbeKWiBtbMzg7MjsxMDI7MTI1OzIzMm3ilogbWzM4OzI7MTAzOzEy'
'NDsyMzFt4pWRG1szODsyOzEwMzsxMjM7MjMwbSAbWzM4OzI7MTAzOzEyMzsyMjlt4paIG1szODsyOzEwMzsxMjI7MjI4beKWiBtb'
'Mzg7MjsxMDQ7MTIxOzIyN23ilogbWzM4OzI7MTA0OzEyMDsyMjZt4paIG1szODsyOzEwNDsxMjA7MjI1beKWiBtbMzg7MjsxMDQ7'
'MTE5OzIyNG3ilogbWzM4OzI7MTA0OzExODsyMjNt4pWUG1szODsyOzEwNTsxMTg7MjIybeKVnRtbMzg7MjsxMDU7MTE3OzIyMW0g'
'G1szODsyOzEwNTsxMTY7MjIwbeKWiBtbMzg7MjsxMDU7MTE2OzIxOW3ilogbWzM4OzI7MTA1OzExNTsyMTht4pWRG1szODsyOzEw'
'NjsxMTQ7MjE3bSAbWzM4OzI7MTA2OzExNDsyMTZt4paIG1szODsyOzEwNjsxMTM7MjE2beKWiBtbMzg7MjsxMDY7MTEyOzIxNW3i'
'logbWzM4OzI7MTA3OzExMjsyMTRt4paIG1szODsyOzEwNzsxMTE7MjEzbeKWiBtbMzg7MjsxMDc7MTEwOzIxMm3ilogbWzM4OzI7'
'MTA3OzEwOTsyMTFt4paIG1szODsyOzEwNzsxMDk7MjEwbeKVlxtbMzg7MjsxMDg7MTA4OzIwOW0gG1szODsyOzEwODsxMDc7MjA4'
'bSAbWzM4OzI7MTA4OzEwNzsyMDdtIBtbMzg7MjsxMDg7MTA2OzIwNm0gG1szODsyOzEwODsxMDU7MjA1bSAbWzM4OzI7MTA5OzEw'
'NTsyMDRt4paIG1szODsyOzEwOTsxMDQ7MjAzbeKWiBtbMzg7MjsxMDk7MTAzOzIwMm3ilZEbWzM4OzI7MTA5OzEwMzsyMDFtIBtb'
'Mzg7MjsxMTA7MTAyOzIwMG0gG1szODsyOzExMDsxMDE7MTk5bSAbWzM4OzI7MTEwOzEwMTsxOThtIBtbMzg7MjsxMTA7MTAwOzE5'
'N20gG1szODsyOzExMDs5OTsxOTZtIBtbMzg7MjsxMTE7OTg7MTk1beKWiBtbMzg7MjsxMTE7OTg7MTk0beKWiBtbMzg7MjsxMTE7'
'OTc7MTkzbeKWiBtbMzg7MjsxMTE7OTY7MTkybeKWiBtbMzg7MjsxMTI7OTY7MTkxbeKWiBtbMzg7MjsxMTI7OTU7MTkwbeKWiBtb'
'Mzg7MjsxMTI7OTQ7MTg5beKWiBtbMzg7MjsxMTI7OTQ7MTg4beKVkRtbMzg7MjsxMTI7OTM7MTg3bSAbWzM4OzI7MTEzOzkyOzE4'
'Nm3ilogbWzM4OzI7MTEzOzkyOzE4NW3ilogbWzM4OzI7MTEzOzkxOzE4NG3ilogbWzM4OzI7MTEzOzkwOzE4M23ilogbWzM4OzI7'
'MTEzOzg5OzE4Mm3ilogbWzM4OzI7MTE0Ozg5OzE4MW3ilZQbWzM4OzI7MTE0Ozg4OzE4MG3ilZ0bWzM4OzI7MTE0Ozg3OzE4MG0g'
'G1szODsyOzExNDs4NzsxNzltIBtbMzg7MjsxMTU7ODY7MTc4beKWiBtbMzg7MjsxMTU7ODU7MTc3beKWiBtbMzg7MjsxMTU7ODU7'
'MTc2beKWiBtbMzg7MjsxMTU7ODQ7MTc1beKWiBtbMzg7MjsxMTU7ODM7MTc0beKWiBtbMzg7MjsxMTY7ODM7MTczbeKVlxtbMzg7'
'MjsxMTY7ODI7MTcybSAbWzM4OzI7MTE2OzgxOzE3MW0gG1szODsyOzExNjs4MTsxNzBtIBtbMzg7MjsxMTY7ODA7MTY5beKWiBtb'
'Mzg7MjsxMTc7Nzk7MTY4beKWiBtbMzg7MjsxMTc7Nzg7MTY3beKWiBtbMzg7MjsxMTc7Nzg7MTY2beKWiBtbMzg7MjsxMTc7Nzc7'
'MTY1beKWiBtbMzg7MjsxMTg7NzY7MTY0beKWiBtbMzg7MjsxMTg7NzY7MTYzbeKWiBtbMzg7MjsxMTg7NzU7MTYybeKVkRtbMzlt'
'ChtbMzg7MjsxMDI7MTI2OzIzNG0gG1szODsyOzEwMjsxMjU7MjMzbeKWiBtbMzg7MjsxMDI7MTI1OzIzMm3ilogbWzM4OzI7MTAz'
'OzEyNDsyMzFt4pWRG1szODsyOzEwMzsxMjM7MjMwbSAbWzM4OzI7MTAzOzEyMzsyMjlt4paIG1szODsyOzEwMzsxMjI7MjI4beKW'
'iBtbMzg7MjsxMDQ7MTIxOzIyN23ilZQbWzM4OzI7MTA0OzEyMDsyMjZt4pWQG1szODsyOzEwNDsxMjA7MjI1beKVkBtbMzg7Mjsx'
'MDQ7MTE5OzIyNG3ilogbWzM4OzI7MTA0OzExODsyMjNt4paIG1szODsyOzEwNTsxMTg7MjIybeKVlxtbMzg7MjsxMDU7MTE3OzIy'
'MW0gG1szODsyOzEwNTsxMTY7MjIwbeKWiBtbMzg7MjsxMDU7MTE2OzIxOW3ilogbWzM4OzI7MTA1OzExNTsyMTht4pWRG1szODsy'
'OzEwNjsxMTQ7MjE3bSAbWzM4OzI7MTA2OzExNDsyMTZt4pWaG1szODsyOzEwNjsxMTM7MjE2beKVkBtbMzg7MjsxMDY7MTEyOzIx'
'NW3ilZAbWzM4OzI7MTA3OzExMjsyMTRt4pWQG1szODsyOzEwNzsxMTE7MjEzbeKVkBtbMzg7MjsxMDc7MTEwOzIxMm3ilogbWzM4'
'OzI7MTA3OzEwOTsyMTFt4paIG1szODsyOzEwNzsxMDk7MjEwbeKVkRtbMzg7MjsxMDg7MTA4OzIwOW0gG1szODsyOzEwODsxMDc7'
'MjA4bSAbWzM4OzI7MTA4OzEwNzsyMDdtIBtbMzg7MjsxMDg7MTA2OzIwNm0gG1szODsyOzEwODsxMDU7MjA1bSAbWzM4OzI7MTA5'
'OzEwNTsyMDRt4paIG1szODsyOzEwOTsxMDQ7MjAzbeKWiBtbMzg7MjsxMDk7MTAzOzIwMm3ilZEbWzM4OzI7MTA5OzEwMzsyMDFt'
'IBtbMzg7MjsxMTA7MTAyOzIwMG0gG1szODsyOzExMDsxMDE7MTk5bSAbWzM4OzI7MTEwOzEwMTsxOThtIBtbMzg7MjsxMTA7MTAw'
'OzE5N20gG1szODsyOzExMDs5OTsxOTZtIBtbMzg7MjsxMTE7OTg7MTk1beKWiBtbMzg7MjsxMTE7OTg7MTk0beKWiBtbMzg7Mjsx'
'MTE7OTc7MTkzbeKVlBtbMzg7MjsxMTE7OTY7MTkybeKVkBtbMzg7MjsxMTI7OTY7MTkxbeKVkBtbMzg7MjsxMTI7OTU7MTkwbeKW'
'iBtbMzg7MjsxMTI7OTQ7MTg5beKWiBtbMzg7MjsxMTI7OTQ7MTg4beKVkRtbMzg7MjsxMTI7OTM7MTg3bSAbWzM4OzI7MTEzOzky'
'OzE4Nm3ilogbWzM4OzI7MTEzOzkyOzE4NW3ilogbWzM4OzI7MTEzOzkxOzE4NG3ilZQbWzM4OzI7MTEzOzkwOzE4M23ilZAbWzM4'
'OzI7MTEzOzg5OzE4Mm3ilogbWzM4OzI7MTE0Ozg5OzE4MW3ilogbWzM4OzI7MTE0Ozg4OzE4MG3ilZcbWzM4OzI7MTE0Ozg3OzE4'
'MG0gG1szODsyOzExNDs4NzsxNzltIBtbMzg7MjsxMTU7ODY7MTc4beKWiBtbMzg7MjsxMTU7ODU7MTc3beKWiBtbMzg7MjsxMTU7'
'ODU7MTc2beKVlBtbMzg7MjsxMTU7ODQ7MTc1beKVkBtbMzg7MjsxMTU7ODM7MTc0beKVkBtbMzg7MjsxMTY7ODM7MTczbeKVnRtb'
'Mzg7MjsxMTY7ODI7MTcybSAbWzM4OzI7MTE2OzgxOzE3MW0gG1szODsyOzExNjs4MTsxNzBtIBtbMzg7MjsxMTY7ODA7MTY5beKW'
'iBtbMzg7MjsxMTc7Nzk7MTY4beKWiBtbMzg7MjsxMTc7Nzg7MTY3beKVlBtbMzg7MjsxMTc7Nzg7MTY2beKVkBtbMzg7MjsxMTc7'
'Nzc7MTY1beKVkBtbMzg7MjsxMTg7NzY7MTY0beKWiBtbMzg7MjsxMTg7NzY7MTYzbeKWiBtbMzg7MjsxMTg7NzU7MTYybeKVkRtb'
'MzltChtbMzg7MjsxMDI7MTI2OzIzNG0gG1szODsyOzEwMjsxMjU7MjMzbeKWiBtbMzg7MjsxMDI7MTI1OzIzMm3ilogbWzM4OzI7'
'MTAzOzEyNDsyMzFt4pWRG1szODsyOzEwMzsxMjM7MjMwbSAbWzM4OzI7MTAzOzEyMzsyMjlt4paIG1szODsyOzEwMzsxMjI7MjI4'
'beKWiBtbMzg7MjsxMDQ7MTIxOzIyN23ilZEbWzM4OzI7MTA0OzEyMDsyMjZtIBtbMzg7MjsxMDQ7MTIwOzIyNW0gG1szODsyOzEw'
'NDsxMTk7MjI0beKWiBtbMzg7MjsxMDQ7MTE4OzIyM23ilogbWzM4OzI7MTA1OzExODsyMjJt4pWRG1szODsyOzEwNTsxMTc7MjIx'
'bSAbWzM4OzI7MTA1OzExNjsyMjBt4paIG1szODsyOzEwNTsxMTY7MjE5beKWiBtbMzg7MjsxMDU7MTE1OzIxOG3ilZEbWzM4OzI7'
'MTA2OzExNDsyMTdtIBtbMzg7MjsxMDY7MTE0OzIxNm3ilogbWzM4OzI7MTA2OzExMzsyMTZt4paIG1szODsyOzEwNjsxMTI7MjE1'
'beKWiBtbMzg7MjsxMDc7MTEyOzIxNG3ilogbWzM4OzI7MTA3OzExMTsyMTNt4paIG1szODsyOzEwNzsxMTA7MjEybeKWiBtbMzg7'
'MjsxMDc7MTA5OzIxMW3ilogbWzM4OzI7MTA3OzEwOTsyMTBt4pWRG1szODsyOzEwODsxMDg7MjA5bSAbWzM4OzI7MTA4OzEwNzsy'
'MDhtIBtbMzg7MjsxMDg7MTA3OzIwN20gG1szODsyOzEwODsxMDY7MjA2bSAbWzM4OzI7MTA4OzEwNTsyMDVtIBtbMzg7MjsxMDk7'
'MTA1OzIwNG3ilogbWzM4OzI7MTA5OzEwNDsyMDNt4paIG1szODsyOzEwOTsxMDM7MjAybeKWiBtbMzg7MjsxMDk7MTAzOzIwMW3i'
'logbWzM4OzI7MTEwOzEwMjsyMDBt4paIG1szODsyOzExMDsxMDE7MTk5beKWiBtbMzg7MjsxMTA7MTAxOzE5OG3ilogbWzM4OzI7'
'MTEwOzEwMDsxOTdt4pWXG1szODsyOzExMDs5OTsxOTZtIBtbMzg7MjsxMTE7OTg7MTk1beKWiBtbMzg7MjsxMTE7OTg7MTk0beKW'
'iBtbMzg7MjsxMTE7OTc7MTkzbeKVkRtbMzg7MjsxMTE7OTY7MTkybSAbWzM4OzI7MTEyOzk2OzE5MW0gG1szODsyOzExMjs5NTsx'
'OTBt4paIG1szODsyOzExMjs5NDsxODlt4paIG1szODsyOzExMjs5NDsxODht4pWRG1szODsyOzExMjs5MzsxODdtIBtbMzg7Mjsx'
'MTM7OTI7MTg2beKWiBtbMzg7MjsxMTM7OTI7MTg1beKWiBtbMzg7MjsxMTM7OTE7MTg0beKVkRtbMzg7MjsxMTM7OTA7MTgzbSAb'
'WzM4OzI7MTEzOzg5OzE4Mm0gG1szODsyOzExNDs4OTsxODFt4paIG1szODsyOzExNDs4ODsxODBt4paIG1szODsyOzExNDs4Nzsx'
'ODBt4pWXG1szODsyOzExNDs4NzsxNzltIBtbMzg7MjsxMTU7ODY7MTc4beKWiBtbMzg7MjsxMTU7ODU7MTc3beKWiBtbMzg7Mjsx'
'MTU7ODU7MTc2beKWiBtbMzg7MjsxMTU7ODQ7MTc1beKWiBtbMzg7MjsxMTU7ODM7MTc0beKWiBtbMzg7MjsxMTY7ODM7MTczbeKW'
'iBtbMzg7MjsxMTY7ODI7MTcybeKWiBtbMzg7MjsxMTY7ODE7MTcxbeKVlxtbMzg7MjsxMTY7ODE7MTcwbSAbWzM4OzI7MTE2Ozgw'
'OzE2OW3ilogbWzM4OzI7MTE3Ozc5OzE2OG3ilogbWzM4OzI7MTE3Ozc4OzE2N23ilZEbWzM4OzI7MTE3Ozc4OzE2Nm0gG1szODsy'
'OzExNzs3NzsxNjVtIBtbMzg7MjsxMTg7NzY7MTY0beKWiBtbMzg7MjsxMTg7NzY7MTYzbeKWiBtbMzg7MjsxMTg7NzU7MTYybeKV'
'kRtbMzltChtbMzg7MjsxMDI7MTI2OzIzNG0gG1szODsyOzEwMjsxMjU7MjMzbeKVmhtbMzg7MjsxMDI7MTI1OzIzMm3ilZAbWzM4'
'OzI7MTAzOzEyNDsyMzFt4pWdG1szODsyOzEwMzsxMjM7MjMwbSAbWzM4OzI7MTAzOzEyMzsyMjlt4pWaG1szODsyOzEwMzsxMjI7'
'MjI4beKVkBtbMzg7MjsxMDQ7MTIxOzIyN23ilZ0bWzM4OzI7MTA0OzEyMDsyMjZtIBtbMzg7MjsxMDQ7MTIwOzIyNW0gG1szODsy'
'OzEwNDsxMTk7MjI0beKVmhtbMzg7MjsxMDQ7MTE4OzIyM23ilZAbWzM4OzI7MTA1OzExODsyMjJt4pWdG1szODsyOzEwNTsxMTc7'
'MjIxbSAbWzM4OzI7MTA1OzExNjsyMjBt4pWaG1szODsyOzEwNTsxMTY7MjE5beKVkBtbMzg7MjsxMDU7MTE1OzIxOG3ilZ0bWzM4'
'OzI7MTA2OzExNDsyMTdtIBtbMzg7MjsxMDY7MTE0OzIxNm3ilZobWzM4OzI7MTA2OzExMzsyMTZt4pWQG1szODsyOzEwNjsxMTI7'
'MjE1beKVkBtbMzg7MjsxMDc7MTEyOzIxNG3ilZAbWzM4OzI7MTA3OzExMTsyMTNt4pWQG1szODsyOzEwNzsxMTA7MjEybeKVkBtb'
'Mzg7MjsxMDc7MTA5OzIxMW3ilZAbWzM4OzI7MTA3OzEwOTsyMTBt4pWdG1szODsyOzEwODsxMDg7MjA5bSAbWzM4OzI7MTA4OzEw'
'NzsyMDhtIBtbMzg7MjsxMDg7MTA3OzIwN20gG1szODsyOzEwODsxMDY7MjA2bSAbWzM4OzI7MTA4OzEwNTsyMDVtIBtbMzg7Mjsx'
'MDk7MTA1OzIwNG3ilZobWzM4OzI7MTA5OzEwNDsyMDNt4pWQG1szODsyOzEwOTsxMDM7MjAybeKVkBtbMzg7MjsxMDk7MTAzOzIw'
'MW3ilZAbWzM4OzI7MTEwOzEwMjsyMDBt4pWQG1szODsyOzExMDsxMDE7MTk5beKVkBtbMzg7MjsxMTA7MTAxOzE5OG3ilZAbWzM4'
'OzI7MTEwOzEwMDsxOTdt4pWdG1szODsyOzExMDs5OTsxOTZtIBtbMzg7MjsxMTE7OTg7MTk1beKVmhtbMzg7MjsxMTE7OTg7MTk0'
'beKVkBtbMzg7MjsxMTE7OTc7MTkzbeKVnRtbMzg7MjsxMTE7OTY7MTkybSAbWzM4OzI7MTEyOzk2OzE5MW0gG1szODsyOzExMjs5'
'NTsxOTBt4pWaG1szODsyOzExMjs5NDsxODlt4pWQG1szODsyOzExMjs5NDsxODht4pWdG1szODsyOzExMjs5MzsxODdtIBtbMzg7'
'MjsxMTM7OTI7MTg2beKVmhtbMzg7MjsxMTM7OTI7MTg1beKVkBtbMzg7MjsxMTM7OTE7MTg0beKVnRtbMzg7MjsxMTM7OTA7MTgz'
'bSAbWzM4OzI7MTEzOzg5OzE4Mm0gG1szODsyOzExNDs4OTsxODFt4pWaG1szODsyOzExNDs4ODsxODBt4pWQG1szODsyOzExNDs4'
'NzsxODBt4pWdG1szODsyOzExNDs4NzsxNzltIBtbMzg7MjsxMTU7ODY7MTc4beKVmhtbMzg7MjsxMTU7ODU7MTc3beKVkBtbMzg7'
'MjsxMTU7ODU7MTc2beKVkBtbMzg7MjsxMTU7ODQ7MTc1beKVkBtbMzg7MjsxMTU7ODM7MTc0beKVkBtbMzg7MjsxMTY7ODM7MTcz'
'beKVkBtbMzg7MjsxMTY7ODI7MTcybeKVkBtbMzg7MjsxMTY7ODE7MTcxbeKVnRtbMzg7MjsxMTY7ODE7MTcwbSAbWzM4OzI7MTE2'
'OzgwOzE2OW3ilZobWzM4OzI7MTE3Ozc5OzE2OG3ilZAbWzM4OzI7MTE3Ozc4OzE2N23ilZ0bWzM4OzI7MTE3Ozc4OzE2Nm0gG1sz'
'ODsyOzExNzs3NzsxNjVtIBtbMzg7MjsxMTg7NzY7MTY0beKVmhtbMzg7MjsxMTg7NzY7MTYzbeKVkBtbMzg7MjsxMTg7NzU7MTYy'
'beKVnRtbMzltCgobWzM4OzI7MTAyOzEyNjsyMzRtIBtbMzg7MjsxMDI7MTI1OzIzM20gG1szODsyOzEwMjsxMjU7MjMybeKWiBtb'
'Mzg7MjsxMDM7MTI0OzIzMW3ilogbWzM4OzI7MTAzOzEyMzsyMzBt4paIG1szODsyOzEwMzsxMjM7MjI5beKWiBtbMzg7MjsxMDM7'
'MTIyOzIyOG3ilogbWzM4OzI7MTA0OzEyMTsyMjdt4paIG1szODsyOzEwNDsxMjA7MjI2beKVlxtbMzg7MjsxMDQ7MTIwOzIyNW0g'
'G1szODsyOzEwNDsxMTk7MjI0bSAbWzM4OzI7MTA0OzExODsyMjNt4paIG1szODsyOzEwNTsxMTg7MjIybeKWiBtbMzg7MjsxMDU7'
'MTE3OzIyMW3ilZcbWzM4OzI7MTA1OzExNjsyMjBtIBtbMzg7MjsxMDU7MTE2OzIxOW0gG1szODsyOzEwNTsxMTU7MjE4bSAbWzM4'
'OzI7MTA2OzExNDsyMTdt4paIG1szODsyOzEwNjsxMTQ7MjE2beKWiBtbMzg7MjsxMDY7MTEzOzIxNm3ilZcbWzM4OzI7MTA2OzEx'
'MjsyMTVtIBtbMzg7MjsxMDc7MTEyOzIxNG3ilogbWzM4OzI7MTA3OzExMTsyMTNt4paIG1szODsyOzEwNzsxMTA7MjEybeKWiBtb'
'Mzg7MjsxMDc7MTA5OzIxMW3ilogbWzM4OzI7MTA3OzEwOTsyMTBt4paIG1szODsyOzEwODsxMDg7MjA5beKWiBtbMzg7MjsxMDg7'
'MTA3OzIwOG3ilogbWzM4OzI7MTA4OzEwNzsyMDdt4pWXG1szODsyOzEwODsxMDY7MjA2bSAbWzM4OzI7MTA4OzEwNTsyMDVt4paI'
'G1szODsyOzEwOTsxMDU7MjA0beKWiBtbMzg7MjsxMDk7MTA0OzIwM23ilogbWzM4OzI7MTA5OzEwMzsyMDJt4paIG1szODsyOzEw'
'OTsxMDM7MjAxbeKWiBtbMzg7MjsxMTA7MTAyOzIwMG3ilogbWzM4OzI7MTEwOzEwMTsxOTlt4paIG1szODsyOzExMDsxMDE7MTk4'
'beKVlxtbMzltChtbMzg7MjsxMDI7MTI2OzIzNG0gG1szODsyOzEwMjsxMjU7MjMzbeKWiBtbMzg7MjsxMDI7MTI1OzIzMm3ilogb'
'WzM4OzI7MTAzOzEyNDsyMzFt4pWUG1szODsyOzEwMzsxMjM7MjMwbeKVkBtbMzg7MjsxMDM7MTIzOzIyOW3ilZAbWzM4OzI7MTAz'
'OzEyMjsyMjht4pWQG1szODsyOzEwNDsxMjE7MjI3beKWiBtbMzg7MjsxMDQ7MTIwOzIyNm3ilogbWzM4OzI7MTA0OzEyMDsyMjVt'
'4pWXG1szODsyOzEwNDsxMTk7MjI0bSAbWzM4OzI7MTA0OzExODsyMjNt4paIG1szODsyOzEwNTsxMTg7MjIybeKWiBtbMzg7Mjsx'
'MDU7MTE3OzIyMW3ilZEbWzM4OzI7MTA1OzExNjsyMjBtIBtbMzg7MjsxMDU7MTE2OzIxOW0gG1szODsyOzEwNTsxMTU7MjE4bSAb'
'WzM4OzI7MTA2OzExNDsyMTdt4paIG1szODsyOzEwNjsxMTQ7MjE2beKWiBtbMzg7MjsxMDY7MTEzOzIxNm3ilZEbWzM4OzI7MTA2'
'OzExMjsyMTVtIBtbMzg7MjsxMDc7MTEyOzIxNG3ilogbWzM4OzI7MTA3OzExMTsyMTNt4paIG1szODsyOzEwNzsxMTA7MjEybeKV'
'lBtbMzg7MjsxMDc7MTA5OzIxMW3ilZAbWzM4OzI7MTA3OzEwOTsyMTBt4pWQG1szODsyOzEwODsxMDg7MjA5beKVkBtbMzg7Mjsx'
'MDg7MTA3OzIwOG3ilZAbWzM4OzI7MTA4OzEwNzsyMDdt4pWdG1szODsyOzEwODsxMDY7MjA2bSAbWzM4OzI7MTA4OzEwNTsyMDVt'
'4paIG1szODsyOzEwOTsxMDU7MjA0beKWiBtbMzg7MjsxMDk7MTA0OzIwM23ilZQbWzM4OzI7MTA5OzEwMzsyMDJt4pWQG1szODsy'
'OzEwOTsxMDM7MjAxbeKVkBtbMzg7MjsxMTA7MTAyOzIwMG3ilZAbWzM4OzI7MTEwOzEwMTsxOTlt4pWQG1szODsyOzExMDsxMDE7'
'MTk4beKVnRtbMzltChtbMzg7MjsxMDI7MTI2OzIzNG0gG1szODsyOzEwMjsxMjU7MjMzbeKWiBtbMzg7MjsxMDI7MTI1OzIzMm3i'
'logbWzM4OzI7MTAzOzEyNDsyMzFt4pWRG1szODsyOzEwMzsxMjM7MjMwbSAbWzM4OzI7MTAzOzEyMzsyMjltIBtbMzg7MjsxMDM7'
'MTIyOzIyOG0gG1szODsyOzEwNDsxMjE7MjI3beKWiBtbMzg7MjsxMDQ7MTIwOzIyNm3ilogbWzM4OzI7MTA0OzEyMDsyMjVt4pWR'
'G1szODsyOzEwNDsxMTk7MjI0bSAbWzM4OzI7MTA0OzExODsyMjNt4paIG1szODsyOzEwNTsxMTg7MjIybeKWiBtbMzg7MjsxMDU7'
'MTE3OzIyMW3ilZEbWzM4OzI7MTA1OzExNjsyMjBtIBtbMzg7MjsxMDU7MTE2OzIxOW0gG1szODsyOzEwNTsxMTU7MjE4bSAbWzM4'
'OzI7MTA2OzExNDsyMTdt4paIG1szODsyOzEwNjsxMTQ7MjE2beKWiBtbMzg7MjsxMDY7MTEzOzIxNm3ilZEbWzM4OzI7MTA2OzEx'
'MjsyMTVtIBtbMzg7MjsxMDc7MTEyOzIxNG3ilogbWzM4OzI7MTA3OzExMTsyMTNt4paIG1szODsyOzEwNzsxMTA7MjEybeKWiBtb'
'Mzg7MjsxMDc7MTA5OzIxMW3ilogbWzM4OzI7MTA3OzEwOTsyMTBt4paIG1szODsyOzEwODsxMDg7MjA5beKWiBtbMzg7MjsxMDg7'
'MTA3OzIwOG3ilogbWzM4OzI7MTA4OzEwNzsyMDdt4pWXG1szODsyOzEwODsxMDY7MjA2bSAbWzM4OzI7MTA4OzEwNTsyMDVt4paI'
'G1szODsyOzEwOTsxMDU7MjA0beKWiBtbMzg7MjsxMDk7MTA0OzIwM23ilogbWzM4OzI7MTA5OzEwMzsyMDJt4paIG1szODsyOzEw'
'OTsxMDM7MjAxbeKWiBtbMzg7MjsxMTA7MTAyOzIwMG3ilZcbWzM4OzI7MTEwOzEwMTsxOTltIBtbMzg7MjsxMTA7MTAxOzE5OG0g'
'G1szOW0KG1szODsyOzEwMjsxMjY7MjM0bSAbWzM4OzI7MTAyOzEyNTsyMzNt4paIG1szODsyOzEwMjsxMjU7MjMybeKWiBtbMzg7'
'MjsxMDM7MTI0OzIzMW3ilZEbWzM4OzI7MTAzOzEyMzsyMzBtIBtbMzg7MjsxMDM7MTIzOzIyOW0gG1szODsyOzEwMzsxMjI7MjI4'
'bSAbWzM4OzI7MTA0OzEyMTsyMjdt4paIG1szODsyOzEwNDsxMjA7MjI2beKWiBtbMzg7MjsxMDQ7MTIwOzIyNW3ilZEbWzM4OzI7'
'MTA0OzExOTsyMjRtIBtbMzg7MjsxMDQ7MTE4OzIyM23ilogbWzM4OzI7MTA1OzExODsyMjJt4paIG1szODsyOzEwNTsxMTc7MjIx'
'beKVkRtbMzg7MjsxMDU7MTE2OzIyMG0gG1szODsyOzEwNTsxMTY7MjE5bSAbWzM4OzI7MTA1OzExNTsyMThtIBtbMzg7MjsxMDY7'
'MTE0OzIxN23ilogbWzM4OzI7MTA2OzExNDsyMTZt4paIG1szODsyOzEwNjsxMTM7MjE2beKVkRtbMzg7MjsxMDY7MTEyOzIxNW0g'
'G1szODsyOzEwNzsxMTI7MjE0beKVmhtbMzg7MjsxMDc7MTExOzIxM23ilZAbWzM4OzI7MTA3OzExMDsyMTJt4pWQG1szODsyOzEw'
'NzsxMDk7MjExbeKVkBtbMzg7MjsxMDc7MTA5OzIxMG3ilZAbWzM4OzI7MTA4OzEwODsyMDlt4paIG1szODsyOzEwODsxMDc7MjA4'
'beKWiBtbMzg7MjsxMDg7MTA3OzIwN23ilZEbWzM4OzI7MTA4OzEwNjsyMDZtIBtbMzg7MjsxMDg7MTA1OzIwNW3ilogbWzM4OzI7'
'MTA5OzEwNTsyMDRt4paIG1szODsyOzEwOTsxMDQ7MjAzbeKVlBtbMzg7MjsxMDk7MTAzOzIwMm3ilZAbWzM4OzI7MTA5OzEwMzsy'
'MDFt4pWQG1szODsyOzExMDsxMDI7MjAwbeKVnRtbMzg7MjsxMTA7MTAxOzE5OW0gG1szODsyOzExMDsxMDE7MTk4bSAbWzM5bQob'
'WzM4OzI7MTAyOzEyNjsyMzRtIBtbMzg7MjsxMDI7MTI1OzIzM23ilZobWzM4OzI7MTAyOzEyNTsyMzJt4paIG1szODsyOzEwMzsx'
'MjQ7MjMxbeKWiBtbMzg7MjsxMDM7MTIzOzIzMG3ilogbWzM4OzI7MTAzOzEyMzsyMjlt4paIG1szODsyOzEwMzsxMjI7MjI4beKW'
'iBtbMzg7MjsxMDQ7MTIxOzIyN23ilogbWzM4OzI7MTA0OzEyMDsyMjZt4pWUG1szODsyOzEwNDsxMjA7MjI1beKVnRtbMzg7Mjsx'
'MDQ7MTE5OzIyNG0gG1szODsyOzEwNDsxMTg7MjIzbeKVmhtbMzg7MjsxMDU7MTE4OzIyMm3ilogbWzM4OzI7MTA1OzExNzsyMjFt'
'4paIG1szODsyOzEwNTsxMTY7MjIwbeKWiBtbMzg7MjsxMDU7MTE2OzIxOW3ilogbWzM4OzI7MTA1OzExNTsyMTht4paIG1szODsy'
'OzEwNjsxMTQ7MjE3beKWiBtbMzg7MjsxMDY7MTE0OzIxNm3ilZQbWzM4OzI7MTA2OzExMzsyMTZt4pWdG1szODsyOzEwNjsxMTI7'
'MjE1bSAbWzM4OzI7MTA3OzExMjsyMTRt4paIG1szODsyOzEwNzsxMTE7MjEzbeKWiBtbMzg7MjsxMDc7MTEwOzIxMm3ilogbWzM4'
'OzI7MTA3OzEwOTsyMTFt4paIG1szODsyOzEwNzsxMDk7MjEwbeKWiBtbMzg7MjsxMDg7MTA4OzIwOW3ilogbWzM4OzI7MTA4OzEw'
'NzsyMDht4paIG1szODsyOzEwODsxMDc7MjA3beKVkRtbMzg7MjsxMDg7MTA2OzIwNm0gG1szODsyOzEwODsxMDU7MjA1beKWiBtb'
'Mzg7MjsxMDk7MTA1OzIwNG3ilogbWzM4OzI7MTA5OzEwNDsyMDNt4paIG1szODsyOzEwOTsxMDM7MjAybeKWiBtbMzg7MjsxMDk7'
'MTAzOzIwMW3ilogbWzM4OzI7MTEwOzEwMjsyMDBt4paIG1szODsyOzExMDsxMDE7MTk5beKWiBtbMzg7MjsxMTA7MTAxOzE5OG3i'
'lZcbWzM5bQobWzM4OzI7MTAyOzEyNjsyMzRtIBtbMzg7MjsxMDI7MTI1OzIzM20gG1szODsyOzEwMjsxMjU7MjMybeKVmhtbMzg7'
'MjsxMDM7MTI0OzIzMW3ilZAbWzM4OzI7MTAzOzEyMzsyMzBt4pWQG1szODsyOzEwMzsxMjM7MjI5beKVkBtbMzg7MjsxMDM7MTIy'
'OzIyOG3ilZAbWzM4OzI7MTA0OzEyMTsyMjdt4pWQG1szODsyOzEwNDsxMjA7MjI2beKVnRtbMzg7MjsxMDQ7MTIwOzIyNW0gG1sz'
'ODsyOzEwNDsxMTk7MjI0bSAbWzM4OzI7MTA0OzExODsyMjNtIBtbMzg7MjsxMDU7MTE4OzIyMm3ilZobWzM4OzI7MTA1OzExNzsy'
'MjFt4pWQG1szODsyOzEwNTsxMTY7MjIwbeKVkBtbMzg7MjsxMDU7MTE2OzIxOW3ilZAbWzM4OzI7MTA1OzExNTsyMTht4pWQG1sz'
'ODsyOzEwNjsxMTQ7MjE3beKVkBtbMzg7MjsxMDY7MTE0OzIxNm3ilZ0bWzM4OzI7MTA2OzExMzsyMTZtIBtbMzg7MjsxMDY7MTEy'
'OzIxNW0gG1szODsyOzEwNzsxMTI7MjE0beKVmhtbMzg7MjsxMDc7MTExOzIxM23ilZAbWzM4OzI7MTA3OzExMDsyMTJt4pWQG1sz'
'ODsyOzEwNzsxMDk7MjExbeKVkBtbMzg7MjsxMDc7MTA5OzIxMG3ilZAbWzM4OzI7MTA4OzEwODsyMDlt4pWQG1szODsyOzEwODsx'
'MDc7MjA4beKVkBtbMzg7MjsxMDg7MTA3OzIwN23ilZ0bWzM4OzI7MTA4OzEwNjsyMDZtIBtbMzg7MjsxMDg7MTA1OzIwNW3ilZob'
'WzM4OzI7MTA5OzEwNTsyMDRt4pWQG1szODsyOzEwOTsxMDQ7MjAzbeKVkBtbMzg7MjsxMDk7MTAzOzIwMm3ilZAbWzM4OzI7MTA5'
'OzEwMzsyMDFt4pWQG1szODsyOzExMDsxMDI7MjAwbeKVkBtbMzg7MjsxMTA7MTAxOzE5OW3ilZAbWzM4OzI7MTEwOzEwMTsxOTht'
'4pWdG1szOW0K'
) -join ''
$BannerPlain = @'
 ██╗ ██████╗  ██╗ ███████╗     ██╗       █████╗  ██╗  ██╗ ███████╗ ██╗  ██╗
 ██║ ██╔══██╗ ██║ ██╔════╝     ██║      ██╔══██╗ ██║ ██╔╝ ██╔════╝ ██║  ██║
 ██║ ██████╔╝ ██║ ███████╗     ██║      ███████║ █████╔╝  █████╗   ███████║
 ██║ ██╔══██╗ ██║ ╚════██║     ██║      ██╔══██║ ██╔═██╗  ██╔══╝   ██╔══██║
 ██║ ██║  ██║ ██║ ███████║     ███████╗ ██║  ██║ ██║  ██╗ ███████╗ ██║  ██║
 ╚═╝ ╚═╝  ╚═╝ ╚═╝ ╚══════╝     ╚══════╝ ╚═╝  ╚═╝ ╚═╝  ╚═╝ ╚══════╝ ╚═╝  ╚═╝

  ██████╗  ██╗   ██╗ ███████╗ ███████╗
 ██╔═══██╗ ██║   ██║ ██╔════╝ ██╔════╝
 ██║   ██║ ██║   ██║ ███████╗ █████╗  
 ██║   ██║ ██║   ██║ ╚════██║ ██╔══╝  
 ╚██████╔╝ ╚██████╔╝ ███████║ ███████╗
  ╚═════╝   ╚═════╝  ╚══════╝ ╚══════╝
'@

function Say([string]$Text) { Write-Host "  $Text" }
function Ok([string]$Text) {
    if ($UseColor) { Write-Host '  ' -NoNewline; Write-Host ([char]0x2713) -ForegroundColor Green -NoNewline; Write-Host " $Text" }
    else { Write-Host "  + $Text" }
}
function Warn([string]$Text) {
    if ($UseColor) { Write-Host '  ' -NoNewline; Write-Host '!' -ForegroundColor Yellow -NoNewline; Write-Host " $Text" }
    else { Write-Host "  ! $Text" }
}
function Section([string]$Text) {
    Write-Host ''
    if ($UseColor) { Write-Host "  $Text" -ForegroundColor Cyan } else { Write-Host "  $Text" }
}
function Kv([string]$Key, [string]$Value) { Write-Host ('  {0} {1,-15}: {2}' -f [char]0x2022, $Key, $Value) }

# Banner: pre-rendered oh-my-logo art, 1:1 with install.sh — >=128 cols wide,
# >=92 stacked, else plain.
$Cols = 80
try { $Cols = $Host.UI.RawUI.WindowSize.Width } catch {}
if (-not $Cols -or $Cols -le 0) { $Cols = 80 }
Write-Host ''
if ($Cols -ge 77) {
    if ($UseColor) {
        [Console]::Out.Write([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($BannerB64)))
        [Console]::Out.Write([char]10)
    } else {
        Write-Host $BannerPlain
    }
} else {
    Write-Host '  IRIS LAKEHOUSE'
}
Write-Host ''

$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "iris: unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}
Say "Detected platform: windows/$Arch"

# Plan first, actions after; existing iris on PATH = announced upgrade.
$Installed = ''
$Existing = Get-Command iris -ErrorAction SilentlyContinue
if ($Existing) {
    try { $Installed = (& $Existing.Source --version) 2>$null } catch { $Installed = '' }
}
$Dest = if ($env:IRIS_DEST) { $env:IRIS_DEST } else { Join-Path $env:USERPROFILE '.iris\bin' }
Write-Host '  Install plan'
Kv 'OS/Arch' "windows / $Arch"
Kv 'Method' 'Prebuilt static binary'
Kv 'Version' $Requested
if ($Installed) { Kv 'Installed' "$Installed -> upgrading" }
Kv 'Destination' $(if ($env:IRIS_DEST) { $Dest } else { '~\.iris' })

$Asset = "iris_windows_$Arch.zip"
$Tmp = Join-Path ([IO.Path]::GetTempPath()) "iris-install-$([IO.Path]::GetRandomFileName())"
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null
try {
    Section '[1/4] Downloading'
    Say "- Fetching $Asset"
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/$Asset" -OutFile (Join-Path $Tmp $Asset)
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/checksums.txt" -OutFile (Join-Path $Tmp 'checksums.txt')

    $WantLine = Select-String -Path (Join-Path $Tmp 'checksums.txt') -Pattern ([regex]::Escape($Asset) + '$') | Select-Object -First 1
    if (-not $WantLine) { throw "iris: checksums.txt has no entry for $Asset" }
    $Want = ($WantLine.Line -split '\s+')[0].ToLowerInvariant()
    $Got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $Tmp $Asset)).Hash.ToLowerInvariant()
    if ($Want -ne $Got) { throw "iris: checksum mismatch for ${Asset}: want $Want, got $Got" }
    Ok 'Verifying checksum... Verified'

    Section '[2/4] Installing'
    Say '- Extracting binary...'
    Expand-Archive -Path (Join-Path $Tmp $Asset) -DestinationPath $Tmp -Force
    New-Item -ItemType Directory -Path $Dest -Force | Out-Null
    $Bin = Join-Path $Dest 'iris.exe'
    # A running iris.exe cannot be overwritten in place, but it can be renamed.
    if (Test-Path $Bin) {
        Remove-Item "$Bin.old" -ErrorAction SilentlyContinue
        try { Move-Item $Bin "$Bin.old" -Force } catch {}
    }
    Move-Item (Join-Path $Tmp 'iris.exe') $Bin -Force
    Remove-Item "$Bin.old" -ErrorAction SilentlyContinue
    Ok 'Binary installed'

    # Persistence: prepend to the user PATH in the registry when missing, then
    # broadcast WM_SETTINGCHANGE so terminals opened after this pick it up
    # without a relogin.
    $UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $UserPath) { $UserPath = '' }
    $OnPath = ($UserPath -split ';' | Where-Object { $_ -eq $Dest }).Count -gt 0
    if (-not $OnPath -and -not $env:IRIS_DEST) {
        [Environment]::SetEnvironmentVariable('Path', "$Dest;$UserPath", 'User')
        try {
            Add-Type -Namespace IrisInstall -Name Env -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Auto)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
            $result = [UIntPtr]::Zero
            # HWND_BROADCAST, WM_SETTINGCHANGE, SMTO_ABORTIFHUNG
            [IrisInstall.Env]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result) | Out-Null
        } catch {}
        Ok 'PATH configured'
    }
    # Same-shell availability: `irm | iex` and dot-/call-invoked runs share this
    # process, so `iris` works immediately in the invoking terminal too.
    if (($env:Path -split ';' | Where-Object { $_ -eq $Dest }).Count -eq 0) {
        $env:Path = "$Dest;$env:Path"
    }

    Section '[3/4] Engine Setup'
    if ($Requested -like 'snapshot*') {
        Warn 'Snapshot build - features may change; some are experimental.'
        Write-Host ''
    }

    # Setup phases live in the binary (huh + viper-backed config + BT bars).
    # IRIS_ENGINE_SETUP=local|remote|skip and IRIS_SETUP_CATALOGS=public|skip|url still work headless.
    & $Bin setup --phase engine
    if ($LASTEXITCODE -ne 0) { throw 'iris: engine setup failed' }

    Section '[4/4] Catalog'
    & $Bin setup --phase catalog
    if ($LASTEXITCODE -ne 0) { throw 'iris: catalog setup failed' }

    Write-Host ''
    Say 'Iris is ready! Try: iris --help'
    Write-Host ''
} finally {
    Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}
