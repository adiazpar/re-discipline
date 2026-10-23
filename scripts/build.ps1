# Compatibility entry point; the canonical build now runs on every platform.
param([string]$Output = "", [switch]$All, [switch]$Check)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$buildArgs = @('run', './scripts/build.go')
if ($Output) { $buildArgs += @('-output', $Output) }
if ($All) { $buildArgs += '-all' }
if ($Check) { $buildArgs += '-check' }
Push-Location $repo
try {
    & go @buildArgs
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
} finally { Pop-Location }
