param(
    [string]$Prefix = "$HOME\bin",
    [switch]$SkipSetup,
    [switch]$SkipVendorGcc,
    [string]$Version = "latest",
    [string]$Repo = "VeyrForge/codehelper",
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
    $ver = if ($tag.StartsWith("v")) { $tag.Substring(1) } else { $tag }
    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("codehelper-install-" + [guid]::NewGuid().ToString())
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
        $universal = "codehelper_${ver}_windows_universal.zip"
        $url = "https://github.com/$Repo/releases/download/$tag/$universal"
        $archive = Join-Path $tmpDir $universal
        try {
            Write-Host "Downloading release: $url"
            Invoke-WebRequest -Uri $url -OutFile $archive
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
            Write-Host "Universal bundle not available, using per-arch artifact."
        }

        $artifact = "codehelper_${ver}_${os}_${arch}.zip"
        $url = "https://github.com/$Repo/releases/download/$tag/$artifact"
        $archive = Join-Path $tmpDir $artifact
        Write-Host "Downloading release: $url"
        Invoke-WebRequest -Uri $url -OutFile $archive
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
