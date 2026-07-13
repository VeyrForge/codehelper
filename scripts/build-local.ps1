# Build codehelper.exe into THIS repository under .\bin\codehelper.exe (fixed path — no placeholders).
# If gcc is not on PATH, downloads WinLibs into .vendor\winlibs-mingw64 (same as `codehelper update`).
#
# Usage (from repo root or anywhere):
#   powershell -ExecutionPolicy Bypass -File .\scripts\build-local.ps1
#
$ErrorActionPreference = "Stop"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $repoRoot

function Get-DefaultVersion {
  $vf = Join-Path $repoRoot "VERSION"
  if (Test-Path -LiteralPath $vf) {
    return (Get-Content -LiteralPath $vf -TotalCount 1).Trim()
  }
  return "0.0.0"
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  Write-Error "go not found on PATH. Install Go 1.25+ and reopen the terminal."
}

$vendorBin = Join-Path $repoRoot ".vendor\winlibs-mingw64\bin"
$vendorGcc = Join-Path $vendorBin "gcc.exe"

if (-not (Get-Command gcc -ErrorAction SilentlyContinue)) {
  if (-not (Test-Path -LiteralPath $vendorGcc)) {
    $boot = Join-Path $repoRoot "scripts\bootstrap-winlibs.ps1"
    if (-not (Test-Path -LiteralPath $boot)) {
      Write-Error "Missing $boot"
    }
    Write-Host "No gcc on PATH; running WinLibs bootstrap (first run is a large download)..."
    & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $boot -RepoRoot $repoRoot
  }
}

if (-not (Test-Path -LiteralPath $vendorGcc)) {
  Write-Error @"
Still no gcc. Either:
  1) Add MSYS2 UCRT64 bin to PATH (e.g. C:\msys64\ucrt64\bin), then rerun this script, OR
  2) Fix WinLibs bootstrap errors above.
"@
}

$env:PATH = "$vendorBin;$env:PATH"
$env:CGO_ENABLED = "1"

$outDir = Join-Path $repoRoot "bin"
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$out = Join-Path $outDir "codehelper.exe"
$ver = Get-DefaultVersion

Write-Host "Repository: $repoRoot"
Write-Host "Building version $ver ->"
Write-Host "  $out"

go build -trimpath -ldflags "-s -w -X github.com/VeyrForge/codehelper/internal/version.linkVersion=$ver" -o $out ./cmd/codehelper

Write-Host ""
Write-Host "Build OK. Smoke test:"
& $out version
Write-Host ""
Write-Host "Use THIS executable for Cursor / VS Code (Codehelper Executable Path):"
Write-Host "  $out"
Write-Host ""
Write-Host "For current PowerShell session only, prefer repo bin first on PATH:"
Write-Host ('  $env:PATH="{0};$env:PATH"' -f $outDir)
