param([string]$RunDirectory = '.local')
$ErrorActionPreference = 'Stop'
$runPath = (Resolve-Path -LiteralPath $RunDirectory).Path
$statePath = Join-Path $runPath 'process.json'
if (!(Test-Path -LiteralPath $statePath)) { throw 'No process.json; verify the running process manually' }
$state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
$process = Get-CimInstance Win32_Process -Filter "ProcessId=$([int]$state.pid)"
if (!$process) { Write-Output 'Gateway already stopped'; return }
if ($process.ExecutablePath -ne $state.binary) { throw 'PID belongs to another executable; refusing to stop it' }
$running = Get-Process -Id $process.ProcessId
Stop-Process -Id $process.ProcessId
if (!$running.WaitForExit(5000)) { throw 'Gateway did not stop within five seconds' }
Write-Output 'Gateway stopped'
