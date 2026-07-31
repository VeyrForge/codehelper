# Green Engine chat — Windows launcher
# Prefer double-click:  %USERPROFILE%\.green\start-chat.cmd
# Or:  powershell -NoProfile -ExecutionPolicy Bypass -File "$env:USERPROFILE\.green\start-chat.ps1"
#
# Backend selection (automatic via `ge chat serve`):
#   *.gguf  → llama.cpp (auto GPU layers on NVIDIA)
#   *.green → native engine_core (experimental; needs `ge` built with --features gpu for --gpu-layers)
#
# Override: GE_CHAT_MODEL, GE_CHAT_BACKEND=native|gguf, GE_GPU_LAYERS=N, GE_CHAT_MCP=1
# GE_ENGINE_ROOT = Green Engine checkout (enables `ge chat install` GPU rebuild via build_ge_release.py)
#
# Build note: stock `cargo build -p ge` is CPU-only. When CUDA Toolkit / kernels DLL exists:
#   python scripts/build_ge_release.py
#   # or: cargo build --release -p ge --features gpu
# Without --features gpu, native --gpu-layers is ignored with a loud stderr WARNING.
#
# NOTE: Use call operator (&) — Start-Process -ArgumentList splits paths on spaces.

$ErrorActionPreference = "Stop"

$GeHome = if ($env:GE_HOME) { $env:GE_HOME } else { Join-Path $env:USERPROFILE ".green" }
$LocalBin = Join-Path $env:USERPROFILE ".local\bin"
$EngineRoot = if ($env:GE_ENGINE_ROOT) { $env:GE_ENGINE_ROOT } else { "" }

$Backend = if ($env:GE_CHAT_BACKEND) { $env:GE_CHAT_BACKEND.ToLowerInvariant() } else { "auto" }
$Port = if ($env:GE_CHAT_PORT) { $env:GE_CHAT_PORT } else { "8767" }
$McpFlag = $env:GE_CHAT_MCP -eq "1" -or $env:GE_CHAT_MCP -eq "true"

if ($env:GE_CHAT_MODEL) {
  $Model = $env:GE_CHAT_MODEL
} elseif ($Backend -eq "native" -or $Backend -eq "green") {
  $Model = Join-Path $GeHome "models\Llama-3.2-1B.green"
} else {
  $Model = Join-Path $GeHome "models\Llama-3.2-1B-Instruct-Q4_K_M.gguf"
  if (-not (Test-Path -LiteralPath $Model)) {
    $altGreen = Join-Path $GeHome "models\Llama-3.2-1B.green"
    if ($Backend -eq "auto" -and (Test-Path -LiteralPath $altGreen)) {
      $Model = $altGreen
    }
  }
}

if ($env:Path -notlike "*${LocalBin}*") {
  $env:Path = "$LocalBin;$env:Path"
}
if ($EngineRoot -and (Test-Path -LiteralPath $EngineRoot)) {
  $env:GE_ENGINE_ROOT = $EngineRoot
}

$ge = Join-Path $LocalBin "ge.exe"
if (-not (Test-Path -LiteralPath $ge)) {
  Write-Error "ge.exe not found at $ge — build/install Green Engine first."
}
if (-not (Test-Path -LiteralPath $Model)) {
  if ($Model -like "*.green") {
    Write-Error "Native model not found: $Model`n  Pack with green-compress or set GE_CHAT_MODEL."
  } else {
    Write-Error "GGUF model not found: $Model`n  ge pull bartowski/Llama-3.2-1B-Instruct-GGUF --file `"*Q4_K_M.gguf`""
  }
}

$isNative = $Model -like "*.green"
if ($isNative) {
  Write-Host "green-chat: native .green path — GPU offload needs ge built with --features gpu"
  Write-Host "  (python scripts/build_ge_release.py  OR  cargo build --release -p ge --features gpu)"
}
if (-not $isNative) {
  $venvPy = Join-Path $GeHome "chat-venv\Scripts\python.exe"
  if (-not (Test-Path -LiteralPath $venvPy)) {
    Write-Host "chat-venv missing — running: ge chat install"
    & $ge chat install
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  }
}

$serveArgs = @("chat", "serve", "--model", $Model, "--port", $Port)
if ($McpFlag) { $serveArgs += "--mcp" }
# GPU layers: let ge/green_chat auto-detect NVIDIA unless GE_GPU_LAYERS is set.
if ($env:GE_GPU_LAYERS) {
  $serveArgs += @("--gpu-layers", $env:GE_GPU_LAYERS)
}

Write-Host "green-chat: starting ge chat serve"
Write-Host "  backend    = $(if ($isNative) { 'native (.green)' } else { 'gguf (llama.cpp, auto GPU on NVIDIA)' })"
Write-Host "  model      = $Model"
Write-Host "  port       = $Port"
if ($McpFlag) { Write-Host "  mcp        = true (codehelper enrich/routing profile)" }
Write-Host "  POST http://127.0.0.1:$Port/v1/chat/completions"
Write-Host "  Ctrl+C to stop"
Write-Host ""

& $ge @serveArgs
exit $LASTEXITCODE
