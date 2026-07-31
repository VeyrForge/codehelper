<#
.SYNOPSIS
  Reproducible llama.cpp baseline harness (Windows) for Green Engine comparison.

.DESCRIPTION
  Prefers %USERPROFILE%\.green\chat-venv\Scripts\python.exe (llama_cpp).
  Model default: %USERPROFILE%\.green\models\Llama-3.2-1B-Instruct-Q4_K_M.gguf
  Does NOT download models — fails clearly if the 1B GGUF is missing.

  GPU: default layers 0 (fair CPU). Optional: $env:GE_GPU_LAYERS = N

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File .\run_llamacpp_bench.ps1
  powershell -NoProfile -ExecutionPolicy Bypass -File .\run_llamacpp_bench.ps1 -Out results\llamacpp.jsonl
#>
[CmdletBinding()]
param(
  [string]$Model = "",
  [string]$Out = "",
  [int]$MaxTokens = 32,
  [double]$Temperature = 0,
  [int]$GpuLayers = -1,
  [int]$Ctx = 0,
  [int]$Warmup = 1,
  [string]$Python = ""
)

$ErrorActionPreference = "Stop"

$GeHome = if ($env:GE_HOME) { $env:GE_HOME } else { Join-Path $env:USERPROFILE ".green" }
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$PyBench = Join-Path $ScriptDir "run_llamacpp_bench.py"

if (-not (Test-Path -LiteralPath $PyBench)) {
  Write-Error "Missing companion script: $PyBench"
}

if (-not $Model) {
  if ($env:GE_CHAT_MODEL) { $Model = $env:GE_CHAT_MODEL }
  elseif ($env:GE_BENCH_GGUF) { $Model = $env:GE_BENCH_GGUF }
  else { $Model = Join-Path $GeHome "models\Llama-3.2-1B-Instruct-Q4_K_M.gguf" }
}

if (-not (Test-Path -LiteralPath $Model)) {
  Write-Host "ERROR: GGUF model missing (refusing to download)." -ForegroundColor Red
  Write-Host "  expected: $Model"
  Write-Host "  Place Llama-3.2-1B-Instruct-Q4_K_M.gguf under $GeHome\models\"
  Write-Host "  or pass -Model / set GE_CHAT_MODEL to an existing local GGUF."
  exit 1
}

if ($GpuLayers -lt 0) {
  if ($env:GE_GPU_LAYERS) { $GpuLayers = [int]$env:GE_GPU_LAYERS } else { $GpuLayers = 0 }
}
if ($GpuLayers -ne 0) {
  Write-Host "NOTE: gpu-layers=$GpuLayers (fair-CPU baseline default is 0; set GE_GPU_LAYERS)."
}
if ($Ctx -le 0) {
  if ($env:GE_CTX) { $Ctx = [int]$env:GE_CTX } else { $Ctx = 2048 }
}

# Resolve python: explicit > chat-venv > GE_CHAT_PYTHON > PATH python with llama_cpp
function Test-LlamaCpp([string]$py) {
  if (-not $py -or -not (Test-Path -LiteralPath $py)) { return $false }
  & $py -c "import llama_cpp" 2>$null
  return ($LASTEXITCODE -eq 0)
}

$venvPy = Join-Path $GeHome "chat-venv\Scripts\python.exe"
$candidates = @()
if ($Python) { $candidates += $Python }
if ($env:GE_CHAT_PYTHON) { $candidates += $env:GE_CHAT_PYTHON }
$candidates += $venvPy
$candidates += "python"

$Py = $null
foreach ($c in $candidates) {
  if (Test-LlamaCpp $c) { $Py = $c; break }
}

if (-not $Py) {
  Write-Host "ERROR: no Python with llama_cpp found." -ForegroundColor Red
  Write-Host "  expected chat-venv: $venvPy"
  Write-Host "  Fix: ge chat install   (or set -Python / GE_CHAT_PYTHON)"
  Write-Host "  Alternate path: start ge chat serve with this GGUF, then use HTTP benches separately."
  exit 1
}

Write-Host "llamacpp-bench:"
Write-Host "  python     = $Py"
Write-Host "  model      = $Model"
Write-Host "  gpu-layers = $GpuLayers  (0=CPU fair baseline; GE_GPU_LAYERS to override)"
Write-Host "  max-tokens = $MaxTokens  temperature=$Temperature  ctx=$Ctx"
if ($Out) { Write-Host "  out        = $Out" }

$argList = @(
  $PyBench,
  "--model", $Model,
  "--max-tokens", "$MaxTokens",
  "--temperature", "$Temperature",
  "--gpu-layers", "$GpuLayers",
  "--ctx", "$Ctx",
  "--warmup", "$Warmup"
)
if ($Out) {
  $outDir = Split-Path -Parent $Out
  if ($outDir -and -not (Test-Path -LiteralPath $outDir)) {
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null
  }
  $argList += @("--out", $Out)
}

& $Py @argList
exit $LASTEXITCODE
