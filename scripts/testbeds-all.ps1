# testbeds-all.ps1 — Windows helper for scripts/testbeds-all.sh
# Usage: scripts/testbeds-all.ps1 [prepare|eval|stubs|fixture|mega]
param(
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$ArgsRest
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
if (-not $Root) { $Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path }

function Find-Bash {
  # Prefer Git Bash paths before PATH "bash" — Windows Store/WSL bash.exe
  # (C:\Windows\System32\bash.exe) cannot open /f/... MSYS paths.
  $candidates = @(
    $env:CODEHELPER_BASH,
    "C:\Program Files\Git\bin\bash.exe",
    "C:\Program Files\Git\usr\bin\bash.exe",
    "bash"
  ) | Where-Object { $_ }
  foreach ($c in $candidates) {
    if ($c -eq "bash") {
      $cmd = Get-Command bash -ErrorAction SilentlyContinue
      if (-not $cmd) { continue }
      $src = $cmd.Source
      # Skip WSL/Store shim — needs a real Git Bash for this script.
      if ($src -match '(?i)[\\/](System32|SysWOW64)[\\/]bash\.exe$') { continue }
      if ($src -match '(?i)WindowsApps[\\/]') { continue }
      return $src
    }
    if (Test-Path -LiteralPath $c) { return $c }
  }
  return $null
}

$bash = Find-Bash
if (-not $bash) {
  Write-Error "Git Bash not found. Install Git for Windows or set CODEHELPER_BASH to bash.exe"
}

$script = Join-Path $Root "scripts\testbeds-all.sh"
# Prefer Unix path for bash on Git Bash / MSYS
$unixScript = $script -replace '\\', '/'
if ($unixScript -match '^([A-Za-z]):/(.*)$') {
  $unixScript = "/$($Matches[1].ToLower())/$($Matches[2])"
}

Write-Host "testbeds-all.ps1 → $bash $unixScript $($ArgsRest -join ' ')"
# Native bash/go logs on stderr must not trip $ErrorActionPreference=Stop.
$prevEap = $ErrorActionPreference
$ErrorActionPreference = "Continue"
& $bash $unixScript @ArgsRest
$code = $LASTEXITCODE
$ErrorActionPreference = $prevEap
exit $code
