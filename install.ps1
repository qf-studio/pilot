# Pilot installer for Windows
# Usage: irm https://raw.githubusercontent.com/alekspetrov/pilot/main/install.ps1 | iex

$ErrorActionPreference = "Stop"

$REPO = "alekspetrov/pilot"
$BINARY_NAME = "pilot.exe"
$INSTALL_DIR = "$env:LOCALAPPDATA\pilot\bin"

function Write-Info($msg) { Write-Host "[INFO] $msg" -ForegroundColor Green }
function Write-Warn($msg) { Write-Host "[WARN] $msg" -ForegroundColor Yellow }
function Write-Err($msg) { Write-Host "[ERROR] $msg" -ForegroundColor Red; exit 1 }

# Detect architecture
$ARCH = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { Write-Err "32-bit Windows is not supported" }

Write-Host ""
Write-Host "  Pilot Installer (Windows)" -ForegroundColor Cyan
Write-Host "  =========================" -ForegroundColor Cyan
Write-Host ""

# Get latest version
Write-Info "Fetching latest version..."
try {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$REPO/releases/latest" -Headers @{ "User-Agent" = "pilot-installer" }
    $VERSION = $release.tag_name
} catch {
    Write-Err "Failed to fetch latest version: $_"
}
Write-Info "Latest version: $VERSION"

# Download
$ARCHIVE_NAME = "pilot-windows-$ARCH.zip"
$DOWNLOAD_URL = "https://github.com/$REPO/releases/download/$VERSION/$ARCHIVE_NAME"
$TMP_DIR = Join-Path $env:TEMP "pilot-install-$(Get-Random)"
$TMP_ARCHIVE = Join-Path $TMP_DIR $ARCHIVE_NAME

New-Item -ItemType Directory -Path $TMP_DIR -Force | Out-Null

Write-Info "Downloading $DOWNLOAD_URL..."
try {
    Invoke-WebRequest -Uri $DOWNLOAD_URL -OutFile $TMP_ARCHIVE -UseBasicParsing
} catch {
    Remove-Item -Recurse -Force $TMP_DIR -ErrorAction SilentlyContinue
    Write-Err "Failed to download: $_"
}

# Extract
Write-Info "Extracting..."
try {
    Expand-Archive -Path $TMP_ARCHIVE -DestinationPath $TMP_DIR -Force
} catch {
    Remove-Item -Recurse -Force $TMP_DIR -ErrorAction SilentlyContinue
    Write-Err "Failed to extract archive: $_"
}

# Find binary
$EXTRACTED = Join-Path $TMP_DIR $BINARY_NAME
if (-not (Test-Path $EXTRACTED)) {
    # Try without .exe (GoReleaser may name it "pilot")
    $EXTRACTED_ALT = Join-Path $TMP_DIR "pilot"
    if (Test-Path $EXTRACTED_ALT) {
        Rename-Item $EXTRACTED_ALT $EXTRACTED
    } else {
        Remove-Item -Recurse -Force $TMP_DIR -ErrorAction SilentlyContinue
        Write-Err "Binary not found in archive"
    }
}

# Install
if (-not (Test-Path $INSTALL_DIR)) {
    Write-Info "Creating $INSTALL_DIR..."
    New-Item -ItemType Directory -Path $INSTALL_DIR -Force | Out-Null
}

Write-Info "Installing to $INSTALL_DIR..."
Copy-Item -Path $EXTRACTED -Destination (Join-Path $INSTALL_DIR $BINARY_NAME) -Force

# Cleanup
Remove-Item -Recurse -Force $TMP_DIR -ErrorAction SilentlyContinue

# Add to PATH if not already there
$currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($currentPath -notlike "*$INSTALL_DIR*") {
    Write-Info "Adding $INSTALL_DIR to user PATH..."
    [Environment]::SetEnvironmentVariable("Path", "$INSTALL_DIR;$currentPath", "User")
    $env:Path = "$INSTALL_DIR;$env:Path"
    $PATH_UPDATED = $true
} else {
    Write-Info "PATH already configured"
    $PATH_UPDATED = $false
}

# Verify
$pilotPath = Join-Path $INSTALL_DIR $BINARY_NAME
if (Test-Path $pilotPath) {
    try {
        $ver = & $pilotPath version 2>&1
        Write-Info "Verified: $ver"
    } catch {
        Write-Info "Installed (version check skipped)"
    }
}

# Instructions
Write-Host ""
Write-Host "  Pilot installed successfully!" -ForegroundColor Green
Write-Host ""

if ($PATH_UPDATED) {
    Write-Host "  ================================================" -ForegroundColor Yellow
    Write-Host "  PATH was updated. Open a new terminal to use it." -ForegroundColor Yellow
    Write-Host "  ================================================" -ForegroundColor Yellow
    Write-Host ""
}

Write-Host "  Get started:"
Write-Host "    pilot version  # Verify installation"
Write-Host "    pilot init     # Initialize configuration"
Write-Host "    pilot start    # Start the daemon"
Write-Host ""
