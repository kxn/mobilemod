param([string]$Version = 'v0.4.0', [string]$Targets = 'all')
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
Push-Location $root
try {
    & go run ./tools/release -version $Version -targets $Targets -output "dist/releases/$Version"
    if ($LASTEXITCODE -ne 0) { throw 'Release packaging failed' }
} finally {
    Pop-Location
}
