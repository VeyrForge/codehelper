# Build codehelper.exe with CGO (required for tree-sitter parsers).
# MSYS2: install "UCRT64" toolchain — pacman -S mingw-w64-ucrt-x86_64-gcc
#
# Usage:
#   .\scripts\build-codehelper.ps1
#   .\scripts\build-codehelper.ps1 -Ucrt64Bin "C:\msys64\ucrt64\bin"
#   $env:CODEHELPER_UCRT64_BIN = "D:\msys64\ucrt64\bin"; .\scripts\build-codehelper.ps1
#
param(
  [string]$Ucrt64Bin = "",
  [string]$OutPath = ""
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

function Get-DefaultVersion {
  $vf = Join-Path $repoRoot "VERSION"
  if (Test-Path -LiteralPath $vf) {
    return (Get-Content -LiteralPath $vf -TotalCount 1).Trim()
  }
  return "0.0.0"
}

$candidates = @()
if ($Ucrt64Bin -ne "") { $candidates += $Ucrt64Bin.TrimEnd('\') }
$envBin = [Environment]::GetEnvironmentVariable("CODEHELPER_UCRT64_BIN")
if ($envBin) { $candidates += $envBin.TrimEnd('\') }
$msys = [Environment]::GetEnvironmentVariable("MSYS2_ROOT")
if ($msys) { $candidates += (Join-Path $msys "ucrt64\bin") }

$candidates += @(
  "C:\msys64\ucrt64\bin",
  "D:\msys64\ucrt64\bin",
  "E:\msys64\ucrt64\bin",
  "$env:USERPROFILE\msys64\ucrt64\bin",
  "$env:LOCALAPPDATA\msys64\ucrt64\bin",
  "F:\msys64\ucrt64\bin"
)

function Test-PathSafe([string]$path) {
  try { return Test-Path -LiteralPath $path } catch { return $false }
}

foreach ($dir in $candidates) {
  if (-not $dir) { continue }
  if ($dir -match '^([A-Za-z]):') {
    $driveRoot = ($Matches[1] + ":\")
    if (-not (Test-Path -LiteralPath $driveRoot)) { continue }
  }
  $gcc = Join-Path $dir "gcc.exe"
  if (Test-PathSafe $gcc) {
    $env:PATH = "$dir;$env:PATH"
    break
  }
}

if (-not (Get-Command gcc -ErrorAction SilentlyContinue)) {
  Write-Error @"
gcc not found. Either:
  - Add MSYS2 UCRT64 bin to PATH (e.g. C:\msys64\ucrt64\bin), or
  - Run: .\scripts\build-codehelper.ps1 -Ucrt64Bin `"C:\msys64\ucrt64\bin`"
  - Or set CODEHELPER_UCRT64_BIN to that folder.
Install compiler: open MSYS2 UCRT64 shell, then: pacman -S mingw-w64-ucrt-x86_64-gcc
"@
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  Write-Error "go not found."
}

$ver = Get-DefaultVersion
$env:CGO_ENABLED = "1"

if ($OutPath -eq "") {
  $OutPath = Join-Path $env:USERPROFILE "bin\codehelper.exe"
}
New-Item -ItemType Directory -Force -Path (Split-Path $OutPath) | Out-Null

go build -trimpath -ldflags "-s -w -X github.com/VeyrForge/codehelper/internal/version.linkVersion=$ver" -o $OutPath ./cmd/codehelper
Write-Host "Built: $OutPath (version $ver)"
& $OutPath version
