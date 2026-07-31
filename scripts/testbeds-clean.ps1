# testbeds-clean.ps1 — Windows helper for scripts/testbeds-clean.sh
# Usage:
#   scripts/testbeds-clean.ps1
#   scripts/testbeds-clean.ps1 --force
#   scripts/testbeds-clean.ps1 --force --reports
#   scripts/testbeds-clean.ps1 --force --keep-real-oss
param(
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$ArgsRest
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
if (-not $Root) { $Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path }

function Find-Bash {
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

$script = Join-Path $Root "scripts\testbeds-clean.sh"
# Prefer Unix path for bash on Git Bash / MSYS
$unixScript = $script -replace '\\', '/'
if ($unixScript -match '^([A-Za-z]):/(.*)$') {
  $unixScript = "/$($Matches[1].ToLower())/$($Matches[2])"
}

Write-Host "testbeds-clean.ps1 → $bash $unixScript $($ArgsRest -join ' ')"
$prevEap = $ErrorActionPreference
$ErrorActionPreference = "Continue"
& $bash $unixScript @ArgsRest
$code = $LASTEXITCODE
$ErrorActionPreference = $prevEap
exit $code
