param(
    [string]$Prefix = "$HOME\bin",
    [switch]$SkipSetup,
    [switch]$SkipVendorGcc,
    [string]$Version = "latest",
    # Empty = VeyrForge/codehelper (public default). Do not infer from cwd git.
    [string]$Repo = "",
    [ValidateSet("auto", "release", "source")]
    [string]$Method = "auto"
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Resolve-Path (Join-Path $scriptDir "..")
if (-not (Test-Path $Prefix)) {
    New-Item -ItemType Directory -Force -Path $Prefix | Out-Null
}
$binDir = Resolve-Path $Prefix
$target = Join-Path $binDir "codehelper.exe"

function Get-CgoWindowsHint {
    @"
CGO/tree-sitter needs gcc.exe reachable from PATH on Windows.

MSYS2 (typical):
  winget install --id MSYS2.MSYS2 -e
  Open 'MSYS2 UCRT64' from Start, run pacman -Syu until idle, then:
    pacman -S --needed base-devel mingw-w64-ucrt-x86_64-toolchain
  Add to User PATH and open a new terminal:
    C:\msys64\ucrt64\bin
  Overview: https://code.visualstudio.com/docs/cpp/config-mingw

"@
}

function Ensure-GccOnPath {
    if (Get-Command gcc -ErrorAction SilentlyContinue) {
        return $true
    }
    $pf86 = [Environment]::GetEnvironmentVariable("ProgramFiles(x86)")
    $candidateBins = @(
        "C:\msys64\ucrt64\bin",
        "C:\msys64\mingw64\bin",
        "C:\msys64\clang64\bin",
        (Join-Path $env:ProgramFiles "Git\mingw64\bin")
    )
    if (-not [string]::IsNullOrWhiteSpace($pf86)) {
        $candidateBins += Join-Path $pf86 "Git\mingw64\bin"
    }
    $candidateBins += Join-Path $HOME "scoop\apps\mingw\current\bin"
    foreach ($bin in $candidateBins) {
        if ([string]::IsNullOrWhiteSpace($bin)) { continue }
        $gccExe = Join-Path $bin "gcc.exe"
        if (-not (Test-Path -LiteralPath $gccExe)) { continue }
        if (($env:PATH -split ";") -notcontains $bin) {
            $env:PATH = "${bin};$env:PATH"
            Write-Host "Prepended GCC to PATH for this install: $bin"
        }
        if (Get-Command gcc -ErrorAction SilentlyContinue) {
            return $true
        }
    }
    return $false
}

function Test-PortableWinLibsApplicable {
    if ($SkipVendorGcc.IsPresent) { return $false }
    try {
        if (-not [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
                [System.Runtime.InteropServices.OSPlatform]::Windows)) {
            return $false
        }
        $cpu = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
        return ($cpu -eq [System.Runtime.InteropServices.Architecture]::X64)
    }
    catch {
        return (($env:OS -eq "Windows_NT") -and ($env:PROCESSOR_ARCHITECTURE -eq "AMD64"))
    }
}

function Get-ReleaseChecksums {
    param(
        [string]$Tag,
        [string]$DestDir
    )
    $sumsUrl = "https://github.com/$Repo/releases/download/$Tag/checksums.txt"
    $sumsFile = Join-Path $DestDir "checksums.txt"
    Write-Host "Downloading checksums: $sumsUrl"
    Invoke-WebRequest -Uri $sumsUrl -OutFile $sumsFile
    return $sumsFile
}

function Assert-FileSha256 {
    param(
        [string]$ArchivePath,
        [string]$ArtifactName,
        [string]$ChecksumsFile
    )
    $expected = $null
    foreach ($line in Get-Content -LiteralPath $ChecksumsFile) {
        $parts = ($line -split '\s+', 2)
        if ($parts.Count -ge 2 -and $parts[1].Trim() -eq $ArtifactName) {
            $expected = $parts[0].Trim().ToLowerInvariant()
            break
        }
    }
    if ([string]::IsNullOrWhiteSpace($expected)) {
        throw "No SHA-256 entry for $ArtifactName in checksums.txt"
    }
    $actual = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "SHA-256 mismatch for $ArtifactName`n  expected: $expected`n  actual:   $actual"
    }
    Write-Host "Checksum OK: $ArtifactName"
    return $expected
}

# Optional Sigstore / GitHub attestation verify (preferred when published).
# Never replaces SHA-256. Live releases through at least v3.0.2 publish
# checksums.txt only — skip honestly when the bundle/attestations are absent.
# $env:CODEHELPER_SKIP_ATTESTATION=1 to skip; CODEHELPER_REQUIRE_ATTESTATION=1
# fails closed if neither cosign nor gh attestation confirmed.
function Assert-OptionalReleaseProvenance {
    param(
        [string]$Tag,
        [string]$ArchivePath,
        [string]$ChecksumsFile,
        [string]$ExpectedSha
    )
    if ($env:CODEHELPER_SKIP_ATTESTATION -eq "1") {
        Write-Host "Optional provenance: skipped (CODEHELPER_SKIP_ATTESTATION=1)."
        return
    }

    $verified = $false
    $destDir = Split-Path -Parent $ArchivePath

    # --- cosign verify-blob on checksums.txt (public releases) ---
    $cosign = Get-Command cosign -ErrorAction SilentlyContinue
    $bundleUrl = "https://github.com/$Repo/releases/download/$Tag/checksums.txt.sigstore.json"
    $bundlePath = Join-Path $destDir "checksums.txt.sigstore.json"
    if ($cosign) {
        $haveBundle = $false
        try {
            Invoke-WebRequest -Uri $bundleUrl -OutFile $bundlePath
            $haveBundle = $true
        }
        catch {
            Write-Host "Optional cosign: no checksums.txt.sigstore.json on this release (checksum-only)."
        }
        if ($haveBundle) {
            $identity = "https://github.com/$Repo/.github/workflows/release.yml@refs/tags/$Tag"
            & cosign verify-blob $ChecksumsFile `
                --bundle $bundlePath `
                --certificate-identity $identity `
                --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
            if ($LASTEXITCODE -ne 0) {
                throw "Cosign verification FAILED for checksums.txt"
            }
            Write-Host "Cosign OK: checksums.txt (keyless Sigstore)"
            $verified = $true
        }
    }
    else {
        Write-Host "Optional cosign: cosign not on PATH (skipped)."
    }

    # --- gh attestation verify on the archive ---
    $gh = Get-Command gh -ErrorAction SilentlyContinue
    if ($gh) {
        $attestPresent = $false
        if (-not [string]::IsNullOrWhiteSpace($ExpectedSha)) {
            $apiUrl = "https://api.github.com/repos/$Repo/attestations/sha256:$ExpectedSha"
            try {
                $null = Invoke-RestMethod -Uri $apiUrl -Headers @{
                    Accept     = "application/vnd.github+json"
                    "User-Agent" = "codehelper-install"
                }
                $attestPresent = $true
            }
            catch {
                $code = $null
                if ($_.Exception.Response -and $_.Exception.Response.StatusCode) {
                    $code = [int]$_.Exception.Response.StatusCode
                }
                if ($code -eq 404) {
                    Write-Host "Optional attestation: none published for this artifact yet (checksum-only)."
                }
                else {
                    $codeLabel = if ($null -ne $code) { "$code" } else { "?" }
                    Write-Host "Optional attestation: GitHub API probe failed (HTTP $codeLabel); skipped."
                }
            }
        }
        if ($attestPresent) {
            & gh attestation verify $ArchivePath --repo $Repo
            if ($LASTEXITCODE -ne 0) {
                throw "Attestation verification FAILED for $(Split-Path -Leaf $ArchivePath)"
            }
            Write-Host "Attestation OK: $(Split-Path -Leaf $ArchivePath)"
            $verified = $true
        }
    }
    else {
        Write-Host "Optional attestation: gh CLI not on PATH (skipped)."
    }

    if ($env:CODEHELPER_REQUIRE_ATTESTATION -eq "1" -and -not $verified) {
        throw "CODEHELPER_REQUIRE_ATTESTATION=1 but neither cosign nor gh attestation succeeded."
    }
}

function Install-FromRelease {
    $arch = "amd64"
    try {
        $cpu = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
        if ($cpu -eq [System.Runtime.InteropServices.Architecture]::Arm64) {
            $arch = "arm64"
        }
        elseif (-not [Environment]::Is64BitOperatingSystem) {
            throw "Only 64-bit Windows (amd64 or arm64) is supported for release artifacts."
        }
    }
    catch {
        if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
            $arch = "arm64"
        }
        elseif ($env:PROCESSOR_ARCHITECTURE -ne "AMD64") {
            throw "Only 64-bit Windows (amd64 or arm64) is supported for release artifacts."
        }
    }
    $os = "windows"
    $tag = $Version
    if ($tag -eq "latest") {
        $latest = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
        $tag = $latest.tag_name
    }
    if ([string]::IsNullOrWhiteSpace($tag)) {
        throw "Could not resolve release tag."
    }
    # Accept Version=3.0.3 or Version=v3.0.3 (GitHub release tags are v-prefixed).
    if (-not $tag.StartsWith("v")) {
        $tag = "v$tag"
    }
    $ver = $tag.Substring(1)
    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("codehelper-install-" + [guid]::NewGuid().ToString())
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
        $sumsFile = Get-ReleaseChecksums -Tag $tag -DestDir $tmpDir
        $universal = "codehelper_${ver}_windows_universal.zip"
        $url = "https://github.com/$Repo/releases/download/$tag/$universal"
        $archive = Join-Path $tmpDir $universal
        try {
            Write-Host "Downloading release: $url"
            Invoke-WebRequest -Uri $url -OutFile $archive
            $expectedSha = Assert-FileSha256 -ArchivePath $archive -ArtifactName $universal -ChecksumsFile $sumsFile
            Assert-OptionalReleaseProvenance -Tag $tag -ArchivePath $archive -ChecksumsFile $sumsFile -ExpectedSha $expectedSha
            Expand-Archive -Path $archive -DestinationPath $tmpDir -Force
            $bundleDir = Get-ChildItem -Path $tmpDir -Directory -Filter "codehelper_*_windows_universal" | Select-Object -First 1
            $installer = if ($bundleDir) { Join-Path $bundleDir.FullName "install.ps1" } else { $null }
            if ($installer -and (Test-Path -LiteralPath $installer)) {
                Write-Host "Installing from universal Windows bundle ($arch)..."
                $installArgs = @{ Prefix = $Prefix }
                if ($SkipSetup.IsPresent) { $installArgs.SkipSetup = $true }
                & $installer @installArgs
                return
            }
        }
        catch {
            Write-Host "Universal bundle not available or checksum/provenance failed, using per-arch artifact."
        }

        $artifact = "codehelper_${ver}_${os}_${arch}.zip"
        $url = "https://github.com/$Repo/releases/download/$tag/$artifact"
        $archive = Join-Path $tmpDir $artifact
        Write-Host "Downloading release: $url"
        Invoke-WebRequest -Uri $url -OutFile $archive
        $expectedSha = Assert-FileSha256 -ArchivePath $archive -ArtifactName $artifact -ChecksumsFile $sumsFile
        Assert-OptionalReleaseProvenance -Tag $tag -ArchivePath $archive -ChecksumsFile $sumsFile -ExpectedSha $expectedSha
        Expand-Archive -Path $archive -DestinationPath $tmpDir -Force
        # The archive contains a versioned subdir with the binaries; locate codehelper.exe.
        $bin = Get-ChildItem -Path $tmpDir -Recurse -Filter "codehelper.exe" | Select-Object -First 1
        if (-not $bin) {
            throw "Release artifact missing codehelper.exe"
        }
        Copy-Item $bin.FullName $target -Force
        # Bundled extras (best-effort): codehelper-mcp + the green engine binaries
        # (ge, greencompress) ship in the same archive so the optional LLM features
        # work out of the box. Absent -> skipped.
        foreach ($extra in @("codehelper-mcp.exe", "ge.exe", "greencompress.exe")) {
            $e = Get-ChildItem -Path $tmpDir -Recurse -Filter $extra | Select-Object -First 1
            if ($e) {
                Copy-Item $e.FullName (Join-Path $binDir $extra) -Force
                Write-Host "Installed $extra -> $binDir"
            }
        }
    } finally {
        Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Install-FromSource {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "go is required for source install fallback (1.25+)."
    }
    Write-Host "Building codehelper from source..."
    $repoStr = $repoRoot.Path
    if (-not (Ensure-GccOnPath)) {
        if (Test-PortableWinLibsApplicable) {
            $boot = Join-Path $scriptDir "bootstrap-winlibs.ps1"
            & $boot -RepoRoot $repoStr
            # Subprocess bootstrap cannot mutate parent PATH; mirror update.go / buildEnvForUpdate.
            $vendorBin = Join-Path $repoStr ".vendor\winlibs-mingw64\bin"
            $vendorGcc = Join-Path $vendorBin "gcc.exe"
            if (Test-Path -LiteralPath $vendorGcc) {
                if (($env:PATH -split ";") -notcontains $vendorBin) {
                    $env:PATH = "$vendorBin;$env:PATH"
                }
            }
        }
    }
    if (-not (Ensure-GccOnPath)) {
        throw @"
gcc not found after searching PATH$(if (-not ($SkipVendorGcc.IsPresent) -and (Test-PortableWinLibsApplicable)) { " and after WinLibs bootstrap" }).

Manual setup: $(Get-CgoWindowsHint)

Or skip auto-download MinGW with -SkipVendorGcc and install gcc yourself.

Or use a release build (prebuilt exe): .\scripts\install.ps1 -Method release
"@
    }
    $goExe = (Get-Command go).Source
    $repoPath = $repoRoot.Path
    $savedCgo = $env:CGO_ENABLED
    Push-Location $repoPath
    try {
        $env:CGO_ENABLED = "1"
        # -tags rod compiles in the headless-browser tier (screenshot/console tools);
        # set CODEHELPER_NO_ROD=1 for a lean build without it.
        $buildArgs = @("build")
        if (-not $env:CODEHELPER_NO_ROD) { $buildArgs += @("-tags", "rod") }
        $buildArgs += @("-o", $target, "./cmd/codehelper")
        # Use & with argument list (not Start-Process): paths with spaces in -o break CreateProcess argument quoting.
        & $goExe $buildArgs
        $exitCode = if ($PSVersionTable.PSVersion.Major -ge 7) { $LASTEXITCODE } else { 0 }
        $failed = if ($PSVersionTable.PSVersion.Major -ge 7) { $exitCode -ne 0 } else { -not $? }
        if ($failed) {
            $codeMsg = if ($PSVersionTable.PSVersion.Major -ge 7) { "exit code $exitCode" } else { "non-zero status" }
            throw @"
go build failed ($codeMsg).
$(Get-CgoWindowsHint)
Or use a prebuilt release: .\scripts\install.ps1 -Method release
"@
        }
        # Match scripts/install.sh: also install codehelper-mcp from source.
        $mcpTarget = Join-Path $binDir "codehelper-mcp.exe"
        $mcpArgs = @("build")
        if (-not $env:CODEHELPER_NO_ROD) { $mcpArgs += @("-tags", "rod") }
        $mcpArgs += @("-o", $mcpTarget, "./cmd/codehelper-mcp")
        & $goExe $mcpArgs
        $mcpExit = if ($PSVersionTable.PSVersion.Major -ge 7) { $LASTEXITCODE } else { 0 }
        $mcpFailed = if ($PSVersionTable.PSVersion.Major -ge 7) { $mcpExit -ne 0 } else { -not $? }
        if ($mcpFailed) {
            $codeMsg = if ($PSVersionTable.PSVersion.Major -ge 7) { "exit code $mcpExit" } else { "non-zero status" }
            throw @"
go build codehelper-mcp failed ($codeMsg).
$(Get-CgoWindowsHint)
Or use a prebuilt release: .\scripts\install.ps1 -Method release
"@
        }
        Write-Host "Installed codehelper-mcp.exe -> $binDir"
    } finally {
        $env:CGO_ENABLED = $savedCgo
        Pop-Location
    }
}

function Ensure-PathLauncher {
    param(
        [Parameter(Mandatory)][string]$ExePath
    )
    $goBin = Join-Path $HOME "go\bin"
    if (-not (Test-Path -LiteralPath $goBin)) {
        return
    }
    $launcher = Join-Path $goBin "codehelper.cmd"
    $launcherBody = @"
@echo off
"$ExePath" %*
"@
    Set-Content -LiteralPath $launcher -Value $launcherBody -Encoding ASCII
    Write-Host "Ensured launcher: $launcher"
}

function Resolve-InstallRepo {
    if (-not [string]::IsNullOrWhiteSpace($script:Repo)) {
        return
    }
    # Public default. Do not infer from cwd git — remote/iex installs have no
    # meaningful clone remote. Preserve VeyrForge casing for Cosign identity.
    $script:Repo = "VeyrForge/codehelper"
}

Resolve-InstallRepo

if ($Method -eq "release") {
    Install-FromRelease
} elseif ($Method -eq "source") {
    Install-FromSource
} else {
    try {
        Install-FromRelease
    } catch {
        Write-Host "Release install failed; falling back to local source build."
        Install-FromSource
    }
}

if (-not (Test-Path -LiteralPath $target)) {
    throw "Install did not produce: $target"
}

Write-Host "Installed: $target"

# Short `ch` alias -> codehelper. codehelper stays the canonical name (MCP
# configs spawn it by name); `ch` is just a faster entrypoint. A copy (not a
# symlink) since Windows symlinks need admin/developer mode. Best-effort.
try {
    Copy-Item $target (Join-Path $binDir "ch.exe") -Force
    Write-Host "Installed ch.exe -> $binDir (alias for codehelper)"
} catch {
    Write-Warning "Could not create 'ch' alias: $($_.Exception.Message)"
}

Ensure-PathLauncher -ExePath $target

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$parts = @()
if (-not [string]::IsNullOrWhiteSpace($userPath)) {
    $parts = $userPath -split ";" | Where-Object { $_ -and ($_ -ne $binDir.Path) }
}
$newPath = (@($binDir.Path) + $parts) -join ";"
[Environment]::SetEnvironmentVariable("Path", $newPath, "User")
if (($env:PATH -split ";") -notcontains $binDir.Path) {
    $env:PATH = "$($binDir.Path);$env:PATH"
}
Write-Host "Ensured $($binDir.Path) is first in your User PATH."

if (-not $SkipSetup.IsPresent) {
    Write-Host "Running codehelper setup..."
    & (Resolve-Path -LiteralPath $target) setup
}

Write-Host ""
Write-Host "Done. Try: codehelper --help"
