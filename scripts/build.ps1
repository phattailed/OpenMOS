# build.ps1 - Build openmos.exe on Windows with zero prerequisites.
#
# If Go is already on PATH it is used as-is. Otherwise a matching Go toolchain is
# downloaded into .gosdk\ inside the repo (no admin rights, no system changes) and
# used only for this build. The result is dist\openmos.exe.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\build.ps1
#
# Optional:
#   -GoVersion 1.24.1   pin a specific Go version (defaults to the module's)

[CmdletBinding()]
param(
    [string]$GoVersion = "1.24.1"
)

$ErrorActionPreference = "Stop"
# Suppress the download progress bar: on large files it floods stdout and can
# stall automated runs.
$ProgressPreference = "SilentlyContinue"

# Resolve repo paths relative to this script, so it works from any working dir.
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot  = Split-Path -Parent $scriptDir
$srcDir    = Join-Path $repoRoot "src"
$distDir   = Join-Path $repoRoot "dist"
$outExe    = Join-Path $distDir "openmos.exe"

function Get-GoExe {
    # Prefer a Go already on PATH.
    $onPath = Get-Command go -ErrorAction SilentlyContinue
    if ($onPath) {
        Write-Host "Using Go on PATH: $($onPath.Source)"
        return $onPath.Source
    }

    # Otherwise use / download a repo-local toolchain.
    $sdkRoot = Join-Path $repoRoot ".gosdk"
    $goExe   = Join-Path $sdkRoot "go\bin\go.exe"
    if (Test-Path $goExe) {
        Write-Host "Using repo-local Go: $goExe"
        return $goExe
    }

    Write-Host "Go not found. Downloading Go $GoVersion (one-time, into .gosdk\)..."
    New-Item -ItemType Directory -Force -Path $sdkRoot | Out-Null
    $zip = Join-Path $sdkRoot "go.zip"
    $url = "https://go.dev/dl/go$GoVersion.windows-amd64.zip"
    Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
    Write-Host "Extracting..."
    Expand-Archive -Path $zip -DestinationPath $sdkRoot -Force
    Remove-Item $zip -Force
    if (-not (Test-Path $goExe)) {
        throw "Go toolchain did not extract as expected at $goExe"
    }
    Write-Host "Go ready: $goExe"
    return $goExe
}

$go = Get-GoExe

New-Item -ItemType Directory -Force -Path $distDir | Out-Null

Write-Host "Building openmos.exe..."
Push-Location $srcDir
try {
    & $go build -o $outExe .
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
} finally {
    Pop-Location
}

Write-Host ""
Write-Host "Built: $outExe" -ForegroundColor Green
Write-Host "Next: generate a config, then run it:"
Write-Host "  dist\openmos.exe --generate-config=config.yaml"
Write-Host "  dist\openmos.exe --config=config.yaml"
