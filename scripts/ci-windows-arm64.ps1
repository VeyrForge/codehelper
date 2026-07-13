# Build and package codehelper for native Windows ARM64 (CI).
# Requires ilammy/msvc-dev-cmd@v1 with arch: arm64 before invoking.
param(
    [Parameter(Mandatory = $true)]
    [string]$Version
)

$ErrorActionPreference = "Stop"

# Git for Windows mingw gcc is x86_64 and breaks ARM64 CGO if Go finds it first.
$env:PATH = ($env:PATH.Split(";") | Where-Object {
    $_ -and $_ -notmatch '(?i)\\mingw|\\msys|\\Git\\usr\\bin|\\Git\\mingw64'
}) -join ";"

$env:CGO_ENABLED = "1"

# Use clang driver with explicit MSVC target. This accepts CGO's GCC-style
# flags (-dM, -fno-stack-protector) and still links against MSVC libs.
if (Get-Command clang -ErrorAction SilentlyContinue) {
    $env:CC = "clang --target=aarch64-pc-windows-msvc"
    $env:CXX = "clang++ --target=aarch64-pc-windows-msvc"
}
else {
    throw "No clang found on PATH for windows/arm64 CGO build."
}

$ldflags = "-s -w -X github.com/VeyrForge/codehelper/internal/version.linkVersion=$Version"

Write-Host "Building codehelper $Version for windows/arm64 (CC=$($env:CC))..."
& go build -trimpath -tags rod -ldflags $ldflags -o codehelper.exe ./cmd/codehelper
if ($LASTEXITCODE -ne 0) { throw "go build codehelper failed with exit $LASTEXITCODE" }
& go build -trimpath -tags rod -ldflags $ldflags -o codehelper-mcp.exe ./cmd/codehelper-mcp
if ($LASTEXITCODE -ne 0) { throw "go build codehelper-mcp failed with exit $LASTEXITCODE" }

$dist = "codehelper_${Version}_windows_arm64"
if (Test-Path $dist) { Remove-Item $dist -Recurse -Force }
New-Item -ItemType Directory -Force -Path $dist | Out-Null
Move-Item codehelper.exe, codehelper-mcp.exe $dist
Copy-Item README.md, LICENSE $dist -ErrorAction SilentlyContinue
if (Test-Path green-bin) { Copy-Item green-bin/* $dist -ErrorAction SilentlyContinue }
Copy-Item third_party/green-engine/LICENSE "$dist/LICENSE-ge" -ErrorAction SilentlyContinue
Copy-Item third_party/green-compress/LICENSE "$dist/LICENSE-greencompress" -ErrorAction SilentlyContinue

@"
codehelper $Version — windows/arm64
=====================================

Extract this folder, then run:

  powershell -ExecutionPolicy Bypass -File install.ps1

(if using the universal zip) or copy binaries from this folder to your PATH.

Then: codehelper setup
In each git repo: codehelper init

Docs: README.md in this folder, or https://github.com/VeyrForge/codehelper
"@ | Set-Content -Path "$dist/INSTALL.txt" -Encoding UTF8

$archive = "${dist}.zip"
if (Test-Path $archive) { Remove-Item $archive -Force }
Compress-Archive -Path $dist -DestinationPath $archive -Force
Write-Host "Created: $archive"
