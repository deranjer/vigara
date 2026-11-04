param([switch]$NonInteractive, [switch]$ResetEnrollment)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$base = Split-Path -Parent $MyInvocation.MyCommand.Path
$legacyWasRunning = $false
$serviceStarted = $false
$serviceWasRunning = $false
$resetBackup = $null
$enrollmentSaved = $false
try {
  $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
  if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run install.cmd and accept the administrator prompt. No enrollment was attempted.'
  }
  $oldTask = Get-ScheduledTask -TaskName VigaraClientUpdate -ErrorAction SilentlyContinue
  if ($oldTask) {
    Disable-ScheduledTask -TaskName VigaraClientUpdate | Out-Null
    if ((Get-ScheduledTask -TaskName VigaraClientUpdate).State -eq 'Running') { throw 'Client updater is running; retry installation after it finishes.' }
  }
  $stateDir = Join-Path $env:ProgramData 'Vigara'
  $statePath = Join-Path $stateDir 'agent.json'
  $legacyState = Join-Path $env:ProgramData 'Vigira\agent.json'
  $installDir = Join-Path $env:ProgramFiles 'Vigara'
  $exe = Join-Path $installDir 'vigara-agent.exe'
  $binaryPath = '"' + $exe + '" --config "' + $statePath + '" run'
  $bootstrapPath = Join-Path $base 'bootstrap.json'
  $reinstall = $false
  $bootstrap = $null
  $dockerEnvironment = @()
  if (Test-Path -LiteralPath $bootstrapPath) {
    $bootstrap = Get-Content -Raw -LiteralPath $bootstrapPath | ConvertFrom-Json
    if ($bootstrap.PSObject.Properties.Name -contains 'reinstall') { $reinstall = [bool]$bootstrap.reinstall }
  }
  if ($bootstrap -and ($bootstrap.PSObject.Properties.Name -contains 'docker') -and $null -ne $bootstrap.docker) {
    . (Join-Path $base 'configure-docker.ps1')
    $dockerEnvironment = @(Get-DockerEnvironment $bootstrap.docker)
    Test-DockerEndpoint $bootstrap.docker
  }
  $savedPath = $statePath
  if (-not (Test-Path -LiteralPath $savedPath)) { $savedPath = $legacyState }
  if (-not $ResetEnrollment -and (Test-Path -LiteralPath $savedPath) -and -not $reinstall -and $bootstrap -and ($bootstrap.PSObject.Properties.Name -contains 'serverUrl') -and -not $NonInteractive) {
    $saved = Get-Content -Raw -LiteralPath $savedPath | ConvertFrom-Json
    Write-Host ('Saved enrollment server: ' + $saved.serverUrl)
    Write-Host ('Bundle enrollment server: ' + $bootstrap.serverUrl)
    Write-Warning 'Reset enrollment disconnects this computer from its previous server and creates a new device identity. Previous history and policies stay on the old server. Local settings and queued reports will not be carried over. Backup files are not deleted.'
    $ResetEnrollment = (Read-Host 'Type RESET to reset enrollment using this bundle, or press Enter to keep the saved enrollment') -ceq 'RESET'
  } elseif ($ResetEnrollment -and -not $NonInteractive) {
    Write-Warning 'Reset enrollment replaces the saved identity and local settings. Previous history and policies stay on the old server. Backup files are not deleted.'
    if ((Read-Host 'Type RESET to confirm enrollment reset') -cne 'RESET') { throw 'Enrollment reset cancelled. Saved enrollment has not been changed.' }
  }
  if ($ResetEnrollment) {
    if ($NonInteractive) { throw 'Enrollment reset requires interactive confirmation. Run install.ps1 -ResetEnrollment without -NonInteractive.' }
    if ($reinstall) { throw 'Enrollment reset requires a fresh Add device bundle, not a Reinstall agent bundle.' }
    if (-not $bootstrap -or -not $bootstrap.token -or -not $bootstrap.serverUrl -or -not $bootstrap.name) {
      throw 'Enrollment reset requires a fresh Add device bundle with bootstrap.json. Saved enrollment has not been changed.'
    }
  }
  $service = Get-Service -Name VigaraAgent -ErrorAction SilentlyContinue
  $legacy = Get-Service -Name VigiraAgent -ErrorAction SilentlyContinue
  if ($service) {
    $serviceWasRunning = $service.Status -ne 'Stopped'
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
  if (-not $ResetEnrollment -and -not (Test-Path -LiteralPath $statePath) -and (Test-Path -LiteralPath $legacyState)) {
    Copy-Item -LiteralPath $legacyState -Destination $statePath
    Write-Host 'Migrated the saved enrollment from Vigira to Vigara.'
  }
  if (Test-Path -LiteralPath (Join-Path $base 'restic.exe')) {
    Copy-Item -LiteralPath (Join-Path $base 'restic.exe') -Destination (Join-Path $installDir 'restic.exe') -Force
    Copy-Item -LiteralPath (Join-Path $base 'restic.LICENSE') -Destination (Join-Path $installDir 'restic.LICENSE') -Force
  }
  if ($ResetEnrollment -and (Test-Path -LiteralPath $savedPath)) {
    $backupPath = Join-Path $stateDir ('agent.before-reset-' + [Guid]::NewGuid().ToString('N') + '.json')
    Copy-Item -LiteralPath $savedPath -Destination $backupPath
    $resetBackup = $backupPath
    if (Test-Path -LiteralPath $statePath) { Remove-Item -LiteralPath $statePath -Force }
    Write-Host ('Previous enrollment backed up to: ' + $resetBackup)
  }
  if ((Test-Path -LiteralPath $statePath) -and $reinstall) {
    $existing = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
    if ($existing.agentId -ne $bootstrap.agentId) { throw 'This reinstall bundle belongs to a different agent. Saved credentials have not been overwritten.' }
  }
  if ((Test-Path -LiteralPath $statePath) -and -not $reinstall) {
    $state = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
    if (-not $state.agentId -or -not $state.credential -or -not $state.serverUrl) { throw 'Saved enrollment is incomplete. It has not been overwritten.' }
    Write-Host 'Reusing the saved enrollment; no new device identity will be created.'
    Write-Host ('Enrollment server: ' + $state.serverUrl)
    if ($bootstrap -and ($bootstrap.PSObject.Properties.Name -contains 'serverUrl') -and $bootstrap.serverUrl -ne $state.serverUrl) {
      Write-Warning 'The saved enrollment points to a different server than this bundle. To move this computer, rerun the installer interactively and choose RESET with a fresh Add device bundle.'
    }
  } else {
    if (-not $bootstrap) { throw 'No saved enrollment or bootstrap.json. Download a reinstall bundle from Device info.' }
    $extraArgs = @()
    if ($bootstrap.insecureHttp) {
      $extraArgs += '--allow-insecure-private-http'
      Write-Warning 'This server uses plain HTTP. Use only on a trusted private network or VPN.'
    }
    if ($bootstrap.PSObject.Properties.Name -contains 'managedBackups' -and $bootstrap.managedBackups) {
      & $exe --config $statePath enable-managed-backups
      if ($LASTEXITCODE -ne 0) { throw 'Could not enable managed backups.' }
    }
    $bootstrap.token | & $exe --config $statePath enroll --server $bootstrap.serverUrl --name $bootstrap.name @extraArgs
    if ($LASTEXITCODE -ne 0) { throw 'Enrollment failed. Keep agent.json. If no credentials were saved, obtain a new reinstall bundle.' }
    if (-not (Test-Path -LiteralPath $statePath)) { throw 'Enrollment did not save agent.json.' }
    $enrollmentSaved = $true
    Remove-Item -LiteralPath $bootstrapPath -Force
  }
  if ($bootstrap -and ($bootstrap.PSObject.Properties.Name -contains 'managedBackups') -and $bootstrap.managedBackups) {
    & $exe --config $statePath enable-managed-backups
    if ($LASTEXITCODE -ne 0) { throw 'Could not enable managed backups.' }
  }
  if (-not $service) {
    New-Service -Name VigaraAgent -DisplayName 'Vigara Agent' -BinaryPathName $binaryPath -StartupType Automatic | Out-Null
    if ($legacyEnvironment) {
      New-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Services\VigaraAgent' -Name Environment -PropertyType MultiString -Value $legacyEnvironment -Force | Out-Null
    }
  } else { Set-Service -Name VigaraAgent -StartupType Automatic }
  if ($dockerEnvironment.Count -gt 0) {
    $key = 'HKLM:\SYSTEM\CurrentControlSet\Services\VigaraAgent'
    $properties = Get-ItemProperty -LiteralPath $key
    $environment = @()
    if ($properties.PSObject.Properties.Name -contains 'Environment') {
      $environment = @($properties.Environment | Where-Object { $_ -notmatch '^VIGIRA_DOCKER_(ENABLED|HOST|MODE)=' })
    }
    New-ItemProperty -LiteralPath $key -Name Environment -PropertyType MultiString -Value @($environment + $dockerEnvironment) -Force | Out-Null
  }
  # This separate, protected helper survives stopping/replacing VigaraAgent.
  if ($bootstrap -and ($bootstrap.PSObject.Properties.Name -contains 'selfUpdates') -and $bootstrap.selfUpdates) {
    $updaterDir = Join-Path $installDir 'updater'
    $updaterExe = Join-Path $updaterDir 'vigara-updater.exe'
    New-Item -ItemType Directory -Force $updaterDir | Out-Null
    icacls.exe $updaterDir /reset | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not reset client updater directory permissions.' }
    icacls.exe $updaterDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not secure the client updater directory.' }
    Copy-Item -LiteralPath $exe -Destination $updaterExe -Force
    & $updaterExe --config $statePath configure-updater
    if ($LASTEXITCODE -ne 0) { throw 'Could not configure client self updates.' }
    $action = New-ScheduledTaskAction -Execute $updaterExe -Argument 'update-client'
    $trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 1)
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Minutes 6) -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    Register-ScheduledTask -TaskName VigaraClientUpdate -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
  }
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
  if ($resetBackup -and -not $enrollmentSaved) {
    try {
      Copy-Item -LiteralPath $resetBackup -Destination $statePath -Force
      Write-Warning 'Enrollment failed; the previous enrollment has been restored. A new bundle may be needed if its token was consumed.'
    } catch {
      Write-Warning ('Could not restore enrollment. Restore it manually from: ' + $resetBackup)
      exit 1
    }
  }
  if ($serviceWasRunning -and -not $serviceStarted -and -not $enrollmentSaved) {
    try { Start-Service -Name VigaraAgent } catch { Write-Warning 'VigaraAgent could not be restarted. Check Windows Services.' }
  }
  if ($legacyWasRunning -and -not $serviceStarted -and -not $enrollmentSaved) {
    try { Start-Service -Name VigiraAgent } catch { Write-Warning 'The legacy service could not be restarted. Check Windows Services.' }
  }
  if (-not $NonInteractive) { Read-Host 'Press Enter to close' | Out-Null }
  exit 1
}
if (-not $NonInteractive) { Read-Host 'Press Enter to close' | Out-Null }
