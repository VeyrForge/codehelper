<#
.SYNOPSIS
  One-command CI regression for GreenEngine after any engine-core change.

.DESCRIPTION
  Runs recovery, release tests (dense + MoE smokes), optional quality diag binaries, decode throughput
  gate (warm decode tok/s, quiet best-of-N; floor >=15, quiet expect ~45), and optional llama.cpp fair compare when available.

  Exit 0 only if every required step passes.

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\ci_regression.ps1
#>
[CmdletBinding()]
param(
  [switch]$SkipRecover,
  [double]$MinDecodeTokS = 15.0,
  # Quiet-host expected warm decode is ~45 tok/s (best-of-3). Gate floor stays 15
  # so load-contaminated CI still passes; use -DecodeBestOf 3 for quiet verify.
  [int]$DecodeBestOf = 3,
  [int]$BenchTokens = 32,
  [string]$BenchModel = "",
  [int]$MinHarnessScore = 23,
  [int]$Q4KmExpectedTop = 48590,
  [switch]$SkipQualityGate
)

$ErrorActionPreference = "Stop"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location -LiteralPath $RepoRoot

function Write-Step([string]$N, [string]$Msg) {
  Write-Host ""
  Write-Host "=== [$N] $Msg ===" -ForegroundColor Cyan
}

function Invoke-Ci([string]$Label, [scriptblock]$Block) {
  Write-Host ">> $Label" -ForegroundColor DarkGray
  & $Block
  if ($LASTEXITCODE -ne 0) {
    throw "Step failed ($Label): exit code $LASTEXITCODE"
  }
}

function Get-ReleaseExe([string]$Name) {
  $exe = Join-Path $RepoRoot ("target\release\{0}.exe" -f $Name)
  if (Test-Path -LiteralPath $exe) { return $exe }
  $alt = Join-Path $RepoRoot ("target\release\{0}" -f $Name)
  if (Test-Path -LiteralPath $alt) { return $alt }
  return $null
}

function Resolve-BenchModel {
  if ($BenchModel -and (Test-Path -LiteralPath $BenchModel)) { return (Resolve-Path -LiteralPath $BenchModel).Path }
  if ($env:GE_BENCH_GREEN -and (Test-Path -LiteralPath $env:GE_BENCH_GREEN)) {
    return (Resolve-Path -LiteralPath $env:GE_BENCH_GREEN).Path
  }
  if ($env:GE_NATIVE_MODEL -and (Test-Path -LiteralPath $env:GE_NATIVE_MODEL)) {
    return (Resolve-Path -LiteralPath $env:GE_NATIVE_MODEL).Path
  }
  $def = Join-Path $env:USERPROFILE ".green\models\Llama-3.2-1B.green"
  if (-not (Test-Path -LiteralPath $def)) {
    throw "1B bench model not found at $def (set -BenchModel or GE_BENCH_GREEN)"
  }
  return (Resolve-Path -LiteralPath $def).Path
}

function Test-LlamaCppPython {
  $py = if ($env:GE_PYTHON) { $env:GE_PYTHON } else { "python" }
  $code = "import importlib.util; raise SystemExit(0 if importlib.util.find_spec('llama_cpp') else 1)"
  & $py -c $code 2>$null
  return ($LASTEXITCODE -eq 0)
}

$failed = $false
try {
  if (-not $SkipRecover) {
    Write-Step "1" "recover_engine_core.py --full"
    Invoke-Ci "python scripts/recover_engine_core.py --full" {
      python (Join-Path $RepoRoot "scripts\recover_engine_core.py") --full
    }
  } else {
    Write-Step "1" "recover_engine_core.py --full (skipped via -SkipRecover)"
  }

  Write-Step "2" "cargo test engine-core lib (release, single-threaded)"
  Invoke-Ci "cargo test --lib" {
    cargo test -p engine-core --lib --release -- --test-threads=1
  }

  Write-Step "3" "cargo test native_generate_smoke (release)"
  Invoke-Ci "native_generate_smoke" {
    cargo test -p engine-core --test native_generate_smoke --release
  }

  Write-Step "3a" "cargo test native_moe_ffn_smoke + native_moe_generate_smoke (release)"
  Invoke-Ci "native_moe_ffn_smoke" {
    cargo test -p engine-core --test native_moe_ffn_smoke --release
  }
  Invoke-Ci "native_moe_generate_smoke" {
    cargo test -p engine-core --test native_moe_generate_smoke --release
  }

  if (-not $SkipQualityGate) {
    Write-Step "3b" "quality gate: Q4_K prefill top $Q4KmExpectedTop + 5-prompt harness (min $MinHarnessScore/25)"
    $qualityGate = Join-Path $RepoRoot "scripts\quality_gate.py"
    if (-not (Test-Path -LiteralPath $qualityGate)) {
      throw "quality_gate.py missing at $qualityGate"
    }
    Invoke-Ci "quality_gate.py --min-score $MinHarnessScore --expected-top $Q4KmExpectedTop" {
      python $qualityGate --min-score $MinHarnessScore --expected-top $Q4KmExpectedTop
    }
  } else {
    Write-Step "3b" "quality gate (skipped via -SkipQualityGate)"
  }

  Write-Step "4" "quality checks (diag / prefill logits if binaries exist)"
  $qualityBins = @(
    "diag_prefill_paths",
    "diag_forward",
    "diag_logits",
    "quality_smoke",
    "quality_regression"
  )
  $ranQuality = $false
  foreach ($bin in $qualityBins) {
    $path = Get-ReleaseExe $bin
    if (-not $path) { continue }
    $ranQuality = $true
    Invoke-Ci $bin {
      & $path
    }
  }
  if (-not $ranQuality) {
    Write-Host "quality: skipped (no diag/quality binaries under target/release)" -ForegroundColor Yellow
  }

  Write-Step "5" "decode_1b_bench n=$BenchTokens best-of-$DecodeBestOf (min $MinDecodeTokS tok/s; quiet expect ~45)"
  $benchExe = Get-ReleaseExe "decode_1b_bench"
  if (-not $benchExe) {
    throw "decode_1b_bench binary missing; run recover/build first"
  }
  $modelPath = Resolve-BenchModel
  $prevIgnore = $env:GE_BENCH_IGNORE_EOS
  $prevRepack = $env:GE_REPACK
  if (-not $env:GE_REPACK) { $env:GE_REPACK = "0" }
  $env:GE_BENCH_IGNORE_EOS = "1"
  $bestWarm = 0.0
  $bestText = ""
  try {
    for ($i = 1; $i -le [Math]::Max(1, $DecodeBestOf); $i++) {
      Write-Host "decode_1b_bench attempt $i/$DecodeBestOf" -ForegroundColor DarkGray
      $benchLines = & $benchExe $modelPath $BenchTokens 2>&1
      $benchExit = $LASTEXITCODE
      $benchText = ($benchLines | Out-String)
      Write-Host $benchText
      if ($benchExit -ne 0) {
        throw "decode_1b_bench crashed or exited with code $benchExit (attempt $i)"
      }
      $warmMatch = [regex]::Match($benchText, "warm:.*?\| decode=([\d.]+) tok/s")
      if (-not $warmMatch.Success) {
        throw "decode_1b_bench: could not parse warm decode tok/s (attempt $i)"
      }
      $warmTps = [double]$warmMatch.Groups[1].Value
      if ($warmTps -gt $bestWarm) { $bestWarm = $warmTps; $bestText = $benchText }
    }
  } finally {
    if ($null -ne $prevIgnore) { $env:GE_BENCH_IGNORE_EOS = $prevIgnore } else { Remove-Item Env:GE_BENCH_IGNORE_EOS -ErrorAction SilentlyContinue }
    if ($null -ne $prevRepack) { $env:GE_REPACK = $prevRepack } else { Remove-Item Env:GE_REPACK -ErrorAction SilentlyContinue }
  }
  Write-Host "best-of-$DecodeBestOf warm decode tok/s = $bestWarm (gate >= $MinDecodeTokS; quiet expect ~45)" -ForegroundColor Green
  if ($bestWarm -lt $MinDecodeTokS) {
    throw "decode_1b_bench best warm decode $bestWarm tok/s < required $MinDecodeTokS tok/s (quiet hosts typically ~45; gate floor 15 is load-contaminated floor)"
  }

  Write-Step "6" "optional run_fair_compare.py vs llama (llama-cpp-python)"
  $fairScript = Join-Path $RepoRoot "scripts\run_fair_compare.py"
  if (-not (Test-Path -LiteralPath $fairScript)) {
    Write-Host "run_fair_compare: skipped (scripts/run_fair_compare.py not present)" -ForegroundColor Yellow
  } elseif (-not (Test-LlamaCppPython)) {
    Write-Host "run_fair_compare: skipped (llama_cpp / llama-cpp-python not importable)" -ForegroundColor Yellow
  } else {
    $py = if ($env:GE_PYTHON) { $env:GE_PYTHON } else { "python" }
    Invoke-Ci "run_fair_compare.py" {
      & $py $fairScript
    }
  }

  Write-Host ""
  Write-Host "=== CI regression: ALL OK ===" -ForegroundColor Green
} catch {
  $failed = $true
  Write-Host ""
  Write-Host "=== CI regression: FAILED ===" -ForegroundColor Red
  Write-Host $_.Exception.Message -ForegroundColor Red
}

if ($failed) { exit 1 }
exit 0
