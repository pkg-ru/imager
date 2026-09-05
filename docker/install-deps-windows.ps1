# install-deps-windows.ps1 - install imager system dependencies on Windows.
#
# - FFmpeg via winget (Gyan.FFmpeg);
# - libvips prebuilt binaries (https://libvips.github.io) - DLLs;
# - ONNX Runtime prebuilt runtime from GitHub releases - DLL + headers.
# All DLLs are placed into $env:LOCALAPPDATA\imager\bin, which is prepended to
# PATH for the current user.
#
# Usage (elevated shell recommended):
#   powershell -ExecutionPolicy Bypass -File docker\install-deps-windows.ps1
$ErrorActionPreference = 'Stop'

$Prefix = '[imager]'
function Log   { Write-Host "$Prefix $($args -join ' ')" }
function Warn  { Write-Host "$Prefix WARNING: $($args -join ' ')" -ForegroundColor Yellow }
function Die   { Write-Host "$Prefix ERROR: $($args -join ' ')" -ForegroundColor Red; exit 1 }

$InstallDir = Join-Path $env:LOCALAPPDATA 'imager\bin'
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

# -- 1. FFmpeg (winget) -----------------------------------------------------
if (Get-Command ffmpeg -ErrorAction SilentlyContinue) {
    Log 'ffmpeg already in PATH - skip'
} else {
    Log 'Installing FFmpeg via winget (Gyan.FFmpeg) ...'
    winget install --id Gyan.FFmpeg --accept-source-agreements --accept-package-agreements --silent
    if ($LASTEXITCODE -ne 0) { Die "winget install Gyan.FFmpeg failed (exit $LASTEXITCODE)" }
}

# -- 2. libvips (prebuilt DLLs) ---------------------------------------------
$VipsVer = '8.16.1'
$VipsBase = "vips-dev-w64-all-$VipsVer"
$VipsZip = "$VipsBase.zip"
$VipsUrl = "https://github.com/libvips/libvips/releases/download/v$VipsVer/$VipsZip"
$VipsExtract = Join-Path $env:TEMP $VipsBase

if (Test-Path (Join-Path $InstallDir 'libvips-42.dll')) {
    Log "libvips already installed ($(Join-Path $InstallDir 'libvips-42.dll')) - skip"
} else {
    Log "Downloading libvips $VipsVer ..."
    $zip = Join-Path $env:TEMP $VipsZip
    Invoke-WebRequest -Uri $VipsUrl -OutFile $zip -UseBasicParsing
    if (Test-Path $VipsExtract) { Remove-Item -Recurse -Force $VipsExtract }
    Expand-Archive -Path $zip -DestinationPath $env:TEMP -Force

    Log "Copying libvips DLLs to $InstallDir ..."
    $src = Join-Path $VipsExtract 'bin'
    if (-not (Test-Path $src)) {
        # fallback: bin unpacked under a versioned subdir
        $src = Join-Path $VipsExtract 'vips-dev-w64-all-*\bin'
        if (-not (Test-Path $src)) { Die "libvips archive layout unexpected under $VipsExtract" }
    }
    Copy-Item -Path (Join-Path $src '*') -Destination $InstallDir -Recurse -Force
    Remove-Item -Recurse -Force $VipsExtract, $zip -ErrorAction SilentlyContinue
    Log 'libvips DLLs installed'
}

# -- 3. ONNX Runtime (prebuilt runtime DLL + headers) -------------------------
$OrtVer = '1.20.2'
$OrtBase = "onnxruntime-win-x64-$OrtVer"
$OrtZip = "$OrtBase.zip"
$OrtUrl = "https://github.com/microsoft/onnxruntime/releases/download/v$OrtVer/$OrtZip"
$OrtExtract = Join-Path $env:TEMP $OrtBase

if (Test-Path (Join-Path $InstallDir 'onnxruntime.dll')) {
    Log "onnxruntime already installed - skip"
} else {
    Log "Downloading ONNX Runtime $OrtVer ..."
    $zip = Join-Path $env:TEMP $OrtZip
    Invoke-WebRequest -Uri $OrtUrl -OutFile $zip -UseBasicParsing
    if (Test-Path $OrtExtract) { Remove-Item -Recurse -Force $OrtExtract }
    Expand-Archive -Path $zip -DestinationPath $env:TEMP -Force

    Log "Copying onnxruntime.dll to $InstallDir ..."
    Copy-Item -Path (Join-Path $OrtExtract 'lib\onnxruntime.dll') -Destination $InstallDir -Force
    Remove-Item -Recurse -Force $OrtExtract, $zip -ErrorAction SilentlyContinue
    Log 'onnxruntime.dll installed'
}

# -- 4. PATH registration (user scope) ---------------------------------------
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$InstallDir", 'User')
    Log "Added $InstallDir to user PATH"
}
# reflect in the current session as well
$env:Path = "$InstallDir;$env:Path"

Log 'Done.'
Log 'Restart your shell so the new PATH is picked up.'
Log 'Next: docker\download-models.sh and docker\install-imager.sh (see docs\INSTALLATION.md).'