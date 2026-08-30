# install-service.ps1 - Register openmos.exe as a Windows service.
#
# Runs OpenMOS in the background, starting automatically on boot, using the
# built-in Windows Service Control Manager (sc.exe). No third-party tools.
#
# Must be run from an elevated (Administrator) PowerShell prompt.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\install-service.ps1
#
# Optional:
#   -ExePath   path to openmos.exe   (default: dist\openmos.exe)
#   -ConfigPath path to config.yaml  (default: config.yaml next to the exe)
#   -ServiceName name of the service (default: OpenMOS)
#   -Uninstall  remove the service instead of installing it

[CmdletBinding()]
param(
    [string]$ExePath,
    [string]$ConfigPath,
    [string]$ServiceName = "OpenMOS",
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

# Require elevation: creating a service needs admin rights.
$isAdmin = ([Security.Principal.WindowsPrincipal] `
    [Security.Principal.WindowsIdentity]::GetCurrent()
).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    throw "This script must be run as Administrator. Right-click PowerShell and 'Run as administrator'."
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot  = Split-Path -Parent $scriptDir

if ($Uninstall) {
    Write-Host "Stopping and removing service '$ServiceName'..."
    & sc.exe stop $ServiceName | Out-Null
    Start-Sleep -Seconds 2
    & sc.exe delete $ServiceName
    if ($LASTEXITCODE -ne 0) { throw "Failed to delete service (exit $LASTEXITCODE)" }
    Write-Host "Service '$ServiceName' removed." -ForegroundColor Green
    return
}

if (-not $ExePath)    { $ExePath = Join-Path $repoRoot "dist\openmos.exe" }
if (-not $ConfigPath) { $ConfigPath = Join-Path (Split-Path -Parent $ExePath) "config.yaml" }

$ExePath    = (Resolve-Path $ExePath).Path
if (-not (Test-Path $ExePath)) { throw "openmos.exe not found at $ExePath. Run scripts\build.ps1 first." }

# Generate a config if none exists yet, so the service has something to load.
if (-not (Test-Path $ConfigPath)) {
    Write-Host "No config at $ConfigPath - generating a default one..."
    & $ExePath "--generate-config=$ConfigPath"
    if ($LASTEXITCODE -ne 0) { throw "Failed to generate default config (exit $LASTEXITCODE)" }
    Write-Host "Generated $ConfigPath - review it before the service handles live traffic." -ForegroundColor Yellow
}

# binPath must quote paths containing spaces. sc.exe is picky: a space is required
# after 'binPath='.
$binPath = "`"$ExePath`" --config=`"$ConfigPath`""

Write-Host "Creating service '$ServiceName'..."
& sc.exe create $ServiceName binPath= $binPath start= auto DisplayName= "OpenMOS Media Object Server"
if ($LASTEXITCODE -ne 0) { throw "sc.exe create failed (exit $LASTEXITCODE)" }

& sc.exe description $ServiceName "MOS gateway bridging ENPS rundowns to downstream systems (e.g. vMix)." | Out-Null

Write-Host "Starting service..."
& sc.exe start $ServiceName
if ($LASTEXITCODE -ne 0) {
    Write-Warning "Service created but did not start. Check the config and Windows Event Log."
} else {
    Write-Host "Service '$ServiceName' installed and started." -ForegroundColor Green
}

Write-Host ""
Write-Host "Manage it with:"
Write-Host "  sc.exe query $ServiceName"
Write-Host "  sc.exe stop $ServiceName"
Write-Host "  powershell -File scripts\install-service.ps1 -Uninstall   # to remove"
