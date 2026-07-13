# Install codehelper from a Windows universal bundle (amd64 + arm64 subdirs).
# Run from the extracted bundle root: powershell -File install.ps1
param(
    [string]$Prefix = "$HOME\bin",
    [switch]$SkipSetup
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$marker = Join-Path $root ".bundle-os"
if (-not (Test-Path $marker)) {
    throw "Missing .bundle-os — not a codehelper universal bundle."
}
$expected = (Get-Content $marker -Raw).Trim()
if ($expected -ne "windows") {
    throw "This bundle is for windows; marker says: $expected"
}

$arch = "amd64"
try {
    $cpu = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
    if ($cpu -eq [System.Runtime.InteropServices.Architecture]::Arm64) {
        $arch = "arm64"
    }
    elseif (-not [Environment]::Is64BitOperatingSystem) {
        throw "Only 64-bit Windows (amd64 or arm64) is supported."
    }
}
catch {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
        $arch = "arm64"
    }
    elseif ($env:PROCESSOR_ARCHITECTURE -ne "AMD64") {
        throw "Only 64-bit Windows (amd64 or arm64) is supported."
    }
}

$src = Join-Path $root $arch
$exe = Join-Path $src "codehelper.exe"
if (-not (Test-Path -LiteralPath $exe)) {
    throw "No binaries for windows/$arch in this bundle."
}

if (-not (Test-Path $Prefix)) {
    New-Item -ItemType Directory -Force -Path $Prefix | Out-Null
}
$binDir = Resolve-Path $Prefix
$target = Join-Path $binDir "codehelper.exe"

Copy-Item $exe $target -Force
foreach ($extra in @("codehelper-mcp.exe", "ge.exe", "greencompress.exe")) {
    $from = Join-Path $src $extra
    if (Test-Path -LiteralPath $from) {
        Copy-Item $from (Join-Path $binDir $extra) -Force
        Write-Host "Installed $extra -> $binDir"
    }
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($userPath -split ";") -notcontains $binDir.Path) {
    [Environment]::SetEnvironmentVariable("Path", "$($binDir.Path);$userPath", "User")
    $env:Path = "$($binDir.Path);$env:Path"
    Write-Host "Added $($binDir.Path) to user PATH"
}

Write-Host "Installed windows/$arch -> $target"

if (-not $SkipSetup.IsPresent) {
    Write-Host "Running codehelper setup..."
    & $target setup --skip-path
}

Write-Host ""
Write-Host "Done. Try: codehelper --help"
