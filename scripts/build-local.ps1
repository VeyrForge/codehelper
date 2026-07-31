# Build codehelper.exe + codehelper-mcp.exe into THIS repository under .\bin\
# (fixed paths — no placeholders). If gcc is not on PATH, downloads WinLibs into
# .vendor\winlibs-mingw64 (same as `codehelper update`).
#
# Default includes -tags rod (headless browser / screenshot MCP tools), matching
# scripts/install.ps1 and release builds. Set CODEHELPER_NO_ROD=1 for a lean build.
# After a rod build, run once:  .\bin\codehelper.exe browser install
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

# Windows: stage as *.new then rename-aside promote so a running MCP can be replaced
# (same pattern as scripts/build-go.mjs / codehelper update).
function Promote-WindowsBuild([string]$StagedPath, [string]$FinalPath) {
  $bak = "$FinalPath.bak"
  if (Test-Path -LiteralPath $bak) {
    Remove-Item -LiteralPath $bak -Force -ErrorAction SilentlyContinue
  }
  if (Test-Path -LiteralPath $FinalPath) {
    try {
      Move-Item -LiteralPath $FinalPath -Destination $bak -Force
    } catch {
      Write-Warning "rename-aside failed ($FinalPath → $bak): $($_.Exception.Message)"
    }
  }
  try {
    Move-Item -LiteralPath $StagedPath -Destination $FinalPath -Force
  } catch {
    # Fallback: copy over if rename into place failed
    Copy-Item -LiteralPath $StagedPath -Destination $FinalPath -Force
    Remove-Item -LiteralPath $StagedPath -Force -ErrorAction SilentlyContinue
  }
  if (Test-Path -LiteralPath $bak) {
    Remove-Item -LiteralPath $bak -Force -ErrorAction SilentlyContinue
  }
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
if (-not $env:GOTOOLCHAIN) {
  $env:GOTOOLCHAIN = "go1.26.5"
}

$outDir = Join-Path $repoRoot "bin"
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$out = Join-Path $outDir "codehelper.exe"
$outMcp = Join-Path $outDir "codehelper-mcp.exe"
$ver = Get-DefaultVersion

Write-Host "Repository: $repoRoot"
Write-Host "GOTOOLCHAIN=$($env:GOTOOLCHAIN)"
Write-Host "Building version $ver ->"
Write-Host "  $out"
Write-Host "  $outMcp"

# -tags rod compiles in the headless-browser tier (screenshot/console tools);
# set CODEHELPER_NO_ROD=1 for a lean build without it (matches install.ps1).
$tagArgs = @()
if (-not $env:CODEHELPER_NO_ROD) {
  $tagArgs = @("-tags", "rod")
  Write-Host "Tags: rod (browser tier on). Set CODEHELPER_NO_ROD=1 to omit."
} else {
  Write-Host "Tags: none (CODEHELPER_NO_ROD set - browser tier off)."
}

$ldflags = "-s -w -X github.com/VeyrForge/codehelper/internal/version.linkVersion=$ver"

$staged = "$out.new"
$buildArgs = @("build", "-trimpath") + $tagArgs + @(
  "-ldflags", $ldflags,
  "-o", $staged,
  "./cmd/codehelper"
)
& go @buildArgs
if ($LASTEXITCODE -ne 0) {
  Write-Error "go build failed (exit $LASTEXITCODE)"
}
Promote-WindowsBuild $staged $out

$stagedMcp = "$outMcp.new"
$mcpArgs = @("build", "-trimpath") + $tagArgs + @(
  "-ldflags", $ldflags,
  "-o", $stagedMcp,
  "./cmd/codehelper-mcp"
)
& go @mcpArgs
if ($LASTEXITCODE -ne 0) {
  Write-Error "go build codehelper-mcp failed (exit $LASTEXITCODE)"
}
Promote-WindowsBuild $stagedMcp $outMcp

Write-Host ""
Write-Host "Build OK. Smoke test:"
& $out version
Write-Host "Also built: $outMcp"
if (-not $env:CODEHELPER_NO_ROD) {
  Write-Host ""
  Write-Host "Browser tier: first time, download managed Chromium (~150MB):"
  Write-Host "  $out browser install"
  Write-Host "Smoke:  $out browser test https://example.com -o .testbeds\reports\browser-smoke.webp"
}
Write-Host ""
Write-Host "Use THIS executable for Cursor / VS Code (Codehelper Executable Path):"
Write-Host "  $out"
Write-Host ""
Write-Host "For current PowerShell session only, prefer repo bin first on PATH:"
Write-Host ('  $env:PATH="{0};$env:PATH"' -f $outDir)
