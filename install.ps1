# Installs the devplat CLI on Windows: downloads the current release from
# get.devplat.ch, verifies its checksum, and puts devplat.exe on your PATH.
#
# Signature verification: unlike install.sh, this script cannot check the
# release signature itself. Windows PowerShell 5.1 runs on .NET Framework, which
# has no Ed25519 and no ImportSubjectPublicKeyInfo, so there is no way to verify
# with what is guaranteed present on the machine. Rather than pretend, this
# script checks the checksum, says plainly what it did and did not check, and
# points at `devplat upgrade` — which does verify the signature, in Go, with no
# external dependency. Every update after the first is therefore covered.
#
# If openssl happens to be on PATH (git-for-windows ships one), the signature is
# checked here too.
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

    # Release signing key. Keep byte-identical with internal/release/release.go
    # and install.sh — pubkey_consistency_test.go fails the build if they drift.
    $ReleasePubKey = @"
-----BEGIN PUBLIC KEY-----
PLACEHOLDER-NO-RELEASE-KEY-CONFIGURED
-----END PUBLIC KEY-----
"@

    if ($ReleasePubKey -match 'PLACEHOLDER-NO-RELEASE-KEY-CONFIGURED') {
        Write-Host "==> NOTE: no release signing key is configured yet; checksum only"
    } elseif (Get-Command openssl -ErrorAction SilentlyContinue) {
        Write-Host "==> verifying release signature"
        $sigPath = Join-Path $tmpDir "checksums.txt.sig"
        Invoke-WebRequest -Uri "$checksumsUrl.sig" -OutFile $sigPath -UseBasicParsing
        $manifestPath = Join-Path $tmpDir "checksums.txt"
        [IO.File]::WriteAllText($manifestPath, $checksums)
        $pubPath = Join-Path $tmpDir "release-pub.pem"
        [IO.File]::WriteAllText($pubPath, $ReleasePubKey)
        & openssl pkeyutl -verify -pubin -inkey $pubPath -rawin -in $manifestPath -sigfile $sigPath | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "SIGNATURE VERIFICATION FAILED - do not install this. Report to security@devplat.ch"
        }
        Write-Host "    signature ok - checksums.txt is authentic"
    } else {
        Write-Host "==> NOTE: openssl not found, so the release SIGNATURE was not checked."
        Write-Host "    The checksum below is verified, but it comes from the same host as the"
        Write-Host "    download. Run 'devplat upgrade' afterwards - that path verifies the"
        Write-Host "    signature itself, with no external tool needed."
    }

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
