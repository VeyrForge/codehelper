# Download portable MinGW (WinLibs) into .vendor/winlibs-mingw64 for CGO builds when gcc is not on PATH.
# Cached under the repository — first run downloads ~200–350 MiB depending on upstream release.
param(
    [Parameter(Mandatory = $true)][string]$RepoRoot
)

$ErrorActionPreference = "Stop"

$vendorRoot = Join-Path $RepoRoot ".vendor\winlibs-mingw64"
$gccExe = Join-Path $vendorRoot "bin\gcc.exe"
if (Test-Path -LiteralPath $gccExe) {
    Write-Host "WinLibs already present: $gccExe"
    exit 0
}

$headers = @{
    "User-Agent" = "codehelper-bootstrap/1.0"
    "Accept"     = "application/vnd.github+json"
}

$rel = Invoke-RestMethod -Uri "https://api.github.com/repos/brechtsanders/winlibs_mingw/releases/latest" -Headers $headers
if (-not $rel.assets) {
    throw "GitHub release has no assets."
}

$asset = $rel.assets | Where-Object { $_.name -match '^winlibs-x86_64-posix-seh-ucrt-.+\.zip$' } | Select-Object -First 1
if (-not $asset) {
    $asset = $rel.assets | Where-Object { $_.name -like '*x86_64*ucrt*.zip' } | Select-Object -First 1
}
if (-not $asset) {
    throw "Could not find a suitable WinLibs x86_64 UCRT zip in latest release."
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("codehelper-winlibs-" + [guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $zipPath = Join-Path $tmp $asset.name
    Write-Host "Downloading $($asset.name) ..."
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zipPath -UseBasicParsing
    Expand-Archive -Path $zipPath -DestinationPath $tmp -Force

    $mingw = Join-Path $tmp "mingw64"
    if (-not (Test-Path -LiteralPath $mingw)) {
        throw "Expected a top-level mingw64 folder inside the WinLibs zip."
    }

    $parent = Split-Path $vendorRoot -Parent
    if (-not (Test-Path $parent)) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
    }
    if (Test-Path -LiteralPath $vendorRoot) {
        Remove-Item -LiteralPath $vendorRoot -Recurse -Force
    }
    Move-Item -LiteralPath $mingw -Destination $vendorRoot
}
finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

if (-not (Test-Path -LiteralPath $gccExe)) {
    throw "After bootstrap, expected gcc at $gccExe"
}
Write-Host "WinLibs ready: $gccExe"
