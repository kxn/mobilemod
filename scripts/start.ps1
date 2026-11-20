param(
    [Parameter(Mandatory)][string]$Binary,
    [Parameter(Mandatory)][string]$Config,
    [string]$RunDirectory = '.local'
)
$ErrorActionPreference = 'Stop'
$binaryPath = (Resolve-Path -LiteralPath $Binary).Path
$configPath = (Resolve-Path -LiteralPath $Config).Path
New-Item -ItemType Directory -Force -Path $RunDirectory | Out-Null
$runPath = (Resolve-Path -LiteralPath $RunDirectory).Path
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
# icacls changes the DACL only; Set-Acl may require SeSecurityPrivilege under
# constrained Windows tokens even when the current user owns the directory.
& icacls.exe $runPath /inheritance:r /grant:r "*$($sid):(OI)(CI)F" | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Could not restrict run directory permissions' }
$otherSids = (Get-Acl -LiteralPath $runPath).Access |
    ForEach-Object { $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value } |
    Where-Object { $_ -ne $sid } | Select-Object -Unique
foreach ($otherSid in $otherSids) {
    & icacls.exe $runPath /remove "*$otherSid" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not remove an extra directory permission' }
}
$pidPath = Join-Path $runPath 'gateway.pid'
if (Test-Path -LiteralPath $pidPath) {
    $existingId = [int](Get-Content -LiteralPath $pidPath -Raw)
    $existing = Get-CimInstance Win32_Process -Filter "ProcessId=$existingId"
    if ($existing -and $existing.ExecutablePath -eq $binaryPath) { throw 'Gateway already running' }
}
& $binaryPath check -config $configPath
if ($LASTEXITCODE -ne 0) { throw 'Configuration check failed' }
$arguments = 'run -config "' + $configPath + '"'
$process = Start-Process -FilePath $binaryPath -ArgumentList $arguments -WindowStyle Hidden `
    -RedirectStandardOutput (Join-Path $runPath 'gateway.stdout.log') `
    -RedirectStandardError (Join-Path $runPath 'gateway.stderr.log') -PassThru
@{pid=$process.Id; binary=$binaryPath} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $runPath 'process.json')
$process.Id | Set-Content -LiteralPath $pidPath
Start-Sleep -Milliseconds 1500
$process.Refresh()
if ($process.HasExited) { throw 'Gateway exited; inspect private stderr log' }
Write-Output "Gateway started, PID $($process.Id)"
