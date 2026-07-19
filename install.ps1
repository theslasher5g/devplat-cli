# Installs the devplat CLI on Windows: downloads the current release from
# get.devplat.ch, verifies its checksum, and puts devplat.exe on your PATH.
#
#   irm https://get.devplat.ch/install.ps1 | iex
#
$ErrorActionPreference = "Stop"

$BaseUrl = if ($env:DEVPLAT_INSTALL_BASE) { $env:DEVPLAT_INSTALL_BASE } else { "https://get.devplat.ch" }
$InstallDir = if ($env:DEVPLAT_INSTALL_DIR) { $env:DEVPLAT_INSTALL_DIR } else { "$env:LOCALAPPDATA\devplat\bin" }

if (-not [Environment]::Is64BitOperatingSystem) {
    Write-Error "devplat: only 64-bit Windows is supported right now."
    exit 1
}

Write-Host "==> resolving latest version"
$version = (Invoke-RestMethod -Uri "$BaseUrl/version.txt").Trim()

$archive = "devplat-$version-windows-amd64.zip"
$archiveUrl = "$BaseUrl/$version/$archive"
$checksumsUrl = "$BaseUrl/$version/checksums.txt"

$tmpDir = Join-Path $env:TEMP ("devplat-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmpDir | Out-Null

try {
    $archivePath = Join-Path $tmpDir $archive
    Write-Host "==> downloading $archive ($version)"
    Invoke-WebRequest -Uri $archiveUrl -OutFile $archivePath

    Write-Host "==> verifying checksum"
    $checksums = (Invoke-WebRequest -Uri $checksumsUrl -UseBasicParsing).Content
    $expectedLine = ($checksums -split "`n") | Where-Object { $_ -match [regex]::Escape($archive) }
    if (-not $expectedLine) { throw "no checksum entry found for $archive" }
    $expectedHash = ($expectedLine.Trim() -split '\s+')[0]
    $actualHash = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash
    if ($actualHash.ToLower() -ne $expectedHash.ToLower()) {
        throw "checksum mismatch: expected $expectedHash, got $actualHash"
    }

    Write-Host "==> extracting"
    Expand-Archive -Path $archivePath -DestinationPath $tmpDir -Force

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Path (Join-Path $tmpDir "devplat.exe") -Destination (Join-Path $InstallDir "devplat.exe") -Force
}
finally {
    Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallDir*") {
    Write-Host "==> adding $InstallDir to your user PATH (open a new terminal to pick it up)"
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
}

Write-Host ""
Write-Host "devplat $version installed to $InstallDir\devplat.exe"
Write-Host ""
Write-Host "Next (in a new terminal):"
Write-Host '  devplat connect --token $env:DEVPLAT_TOKEN'
