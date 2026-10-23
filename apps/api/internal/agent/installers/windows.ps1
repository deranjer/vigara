param([switch]$NonInteractive)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$base = Split-Path -Parent $MyInvocation.MyCommand.Path
$legacyWasRunning = $false
$serviceStarted = $false
try {
  $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
  if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run install.cmd and accept the administrator prompt. No enrollment was attempted.'
  }
  $stateDir = Join-Path $env:ProgramData 'Vigara'
  $statePath = Join-Path $stateDir 'agent.json'
  $legacyState = Join-Path $env:ProgramData 'Vigira\agent.json'
  $installDir = Join-Path $env:ProgramFiles 'Vigara'
  $exe = Join-Path $installDir 'vigara-agent.exe'
  $binaryPath = '"' + $exe + '" --config "' + $statePath + '" run'
  $bootstrapPath = Join-Path $base 'bootstrap.json'
  $service = Get-Service -Name VigaraAgent -ErrorAction SilentlyContinue
  $legacy = Get-Service -Name VigiraAgent -ErrorAction SilentlyContinue
  if ($service) {
    $registered = Get-CimInstance Win32_Service -Filter "Name='VigaraAgent'"
    if ($registered.PathName -ne $binaryPath) { throw 'VigaraAgent uses a custom path. Review its configuration before reinstalling.' }
  }
  $legacyEnvironment = $null
  if ($legacy) {
    $registered = Get-CimInstance Win32_Service -Filter "Name='VigiraAgent'"
    $expected = '"' + (Join-Path $env:ProgramFiles 'Vigira\vigira-agent.exe') + '" --config "' + $legacyState + '" run'
    if ($registered.PathName -ne $expected -or $registered.StartName -ne 'LocalSystem') {
      throw 'The legacy service has a custom account or path. Migrate its configuration manually; it has not been changed.'
    }
    $properties = Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Services\VigiraAgent'
    if ($properties.PSObject.Properties.Name -contains 'Environment') { $legacyEnvironment = $properties.Environment }
    $legacyWasRunning = $legacy.Status -ne 'Stopped'
  }
  # Keep original files and credentials for rollback. Stop before copying state.
  if ($service -and $service.Status -ne 'Stopped') {
    Stop-Service -Name VigaraAgent
    $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
  }
  if ($legacyWasRunning) {
    Stop-Service -Name VigiraAgent
    $legacy.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
  }
  New-Item -ItemType Directory -Force $installDir | Out-Null
  Copy-Item -LiteralPath (Join-Path $base 'vigara-agent.exe') -Destination $exe -Force
  New-Item -ItemType Directory -Force $stateDir | Out-Null
  icacls.exe $stateDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
  if ($LASTEXITCODE -ne 0) { throw 'Could not secure the agent state directory. Enrollment was not attempted.' }
  $probe = Join-Path $stateDir ([IO.Path]::GetRandomFileName())
  try { [IO.File]::WriteAllText($probe, '') } finally {
    if (Test-Path -LiteralPath $probe) { Remove-Item -LiteralPath $probe -Force }
  }
  if (-not (Test-Path -LiteralPath $statePath) -and (Test-Path -LiteralPath $legacyState)) {
    Copy-Item -LiteralPath $legacyState -Destination $statePath
    Write-Host 'Migrated the saved enrollment from Vigira to Vigara.'
  }
  $reinstall = $false
  $bootstrap = $null
  if (Test-Path -LiteralPath $bootstrapPath) {
    $bootstrap = Get-Content -Raw -LiteralPath $bootstrapPath | ConvertFrom-Json
    if ($bootstrap.PSObject.Properties.Name -contains 'reinstall') { $reinstall = [bool]$bootstrap.reinstall }
  }
  if ((Test-Path -LiteralPath $statePath) -and $reinstall) {
    $existing = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
    if ($existing.agentId -ne $bootstrap.agentId) { throw 'This reinstall bundle belongs to a different agent. Saved credentials have not been overwritten.' }
  }
  if ((Test-Path -LiteralPath $statePath) -and -not $reinstall) {
    $state = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
    if (-not $state.agentId -or -not $state.credential -or -not $state.serverUrl) { throw 'Saved enrollment is incomplete. It has not been overwritten.' }
    Write-Host 'Reusing the saved enrollment; no new device identity will be created.'
  } else {
    if (-not $bootstrap) { throw 'No saved enrollment or bootstrap.json. Download a reinstall bundle from Device info.' }
    $extraArgs = @()
    if ($bootstrap.insecureHttp) {
      $extraArgs += '--allow-insecure-private-http'
      Write-Warning 'This server uses plain HTTP. Use only on a trusted private network or VPN.'
    }
    $bootstrap.token | & $exe --config $statePath enroll --server $bootstrap.serverUrl --name $bootstrap.name @extraArgs
    if ($LASTEXITCODE -ne 0) { throw 'Enrollment failed. Keep agent.json. If no credentials were saved, obtain a new reinstall bundle.' }
    if (-not (Test-Path -LiteralPath $statePath)) { throw 'Enrollment did not save agent.json.' }
    Remove-Item -LiteralPath $bootstrapPath -Force
  }
  if (-not $service) {
    New-Service -Name VigaraAgent -DisplayName 'Vigara Agent' -BinaryPathName $binaryPath -StartupType Automatic | Out-Null
    if ($legacyEnvironment) {
      New-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Services\VigaraAgent' -Name Environment -PropertyType MultiString -Value $legacyEnvironment -Force | Out-Null
    }
  } else { Set-Service -Name VigaraAgent -StartupType Automatic }
  Start-Service -Name VigaraAgent
  $service = Get-Service -Name VigaraAgent
  $service.WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
  Start-Sleep -Seconds 2
  $service.Refresh()
  if ($service.Status -ne 'Running') { throw 'VigaraAgent stopped immediately after startup.' }
  $serviceStarted = $true
  if ($legacy) { Set-Service -Name VigiraAgent -StartupType Disabled }
  Write-Host 'Vigara Agent is running as the VigaraAgent Windows service. There is no tray icon.'
  Write-Host ('State: ' + $statePath)
  Write-Host 'Check Vigara for the next successful check-in. Original legacy files remain available for rollback.'
} catch {
  Write-Host ('Installation failed: ' + $_.Exception.Message) -ForegroundColor Red
  if ($legacyWasRunning -and -not $serviceStarted) {
    try { Start-Service -Name VigiraAgent } catch { Write-Warning 'The legacy service could not be restarted. Check Windows Services.' }
  }
  if (-not $NonInteractive) { Read-Host 'Press Enter to close' | Out-Null }
  exit 1
}
if (-not $NonInteractive) { Read-Host 'Press Enter to close' | Out-Null }
