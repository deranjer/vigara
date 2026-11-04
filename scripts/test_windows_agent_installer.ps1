param([ValidateSet('legacy','fresh','updater','failure','wrong-agent','managed','docker','reset-docker','reset','reset-explicit','reset-cancel','reset-typo','reset-legacy','reset-decline','reset-failure','reset-service-failure','reset-missing','reset-reinstall','reset-noninteractive')][string]$Scenario = 'legacy')
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$fixture = Join-Path $repo ('.cache\installer-migration-tests\' + $Scenario + '-' + [Guid]::NewGuid())
if ($Scenario -eq 'managed') { $fixture = Join-Path ([IO.Path]::GetTempPath()) ('vigara-managed-installer-' + [Guid]::NewGuid()) }
New-Item -ItemType Directory -Force $fixture | Out-Null
$env:ProgramData = Join-Path $fixture 'data'
$env:ProgramFiles = Join-Path $fixture 'programs'
$legacyDir = Join-Path $env:ProgramData 'Vigira'
$currentDir = Join-Path $env:ProgramData 'Vigara'
$saved = '{"agentId":"synthetic-agent","credential":"synthetic-credential","serverUrl":"https://example.invalid"}'
New-Item -ItemType Directory -Force $legacyDir,$currentDir | Out-Null
if ($Scenario -in @('fresh','managed','docker','updater') -or ($Scenario.StartsWith('reset') -and $Scenario -ne 'reset-legacy')) { [IO.File]::WriteAllText((Join-Path $currentDir 'agent.json'),$saved) }
else { [IO.File]::WriteAllText((Join-Path $legacyDir 'agent.json'),$saved) }
$bundle = Join-Path $fixture 'bundle'
New-Item -ItemType Directory -Force $bundle | Out-Null
Copy-Item -LiteralPath (Join-Path $repo 'apps\api\internal\agent\installers\windows.ps1') -Destination (Join-Path $bundle 'install.ps1')
# Keep updater setup entirely within the fixture; never register a real task.
$installerPath = Join-Path $bundle 'install.ps1'
$installerText = [IO.File]::ReadAllText($installerPath).Replace('& $updaterExe ', '& Invoke-TestUpdater ')
[IO.File]::WriteAllText($installerPath, $installerText)
if ($Scenario -eq 'updater') { [IO.File]::WriteAllText((Join-Path $bundle 'bootstrap.json'),'{"selfUpdates":true}') }

if ($Scenario.StartsWith('reset')) {
  # Substitute only the executable call in the isolated fixture; no real agent or network is used.
  $script = [IO.File]::ReadAllText((Join-Path $bundle 'install.ps1')).Replace('& $exe ', '& Invoke-TestAgent ')
  [IO.File]::WriteAllText((Join-Path $bundle 'install.ps1'), $script)
  if ($Scenario -ne 'reset-missing') {
    $bootstrap = @{serverUrl='https://production.invalid';token='synthetic-token';name='Production PC';insecureHttp=$false;reinstall=($Scenario -eq 'reset-reinstall');agentId='synthetic-agent'}
    [IO.File]::WriteAllText((Join-Path $bundle 'bootstrap.json'),($bootstrap | ConvertTo-Json))
  }
}
if ($Scenario -ne 'managed') { [IO.File]::WriteAllText((Join-Path $bundle 'vigara-agent.exe'),'synthetic executable, never run') }
if ($Scenario -eq 'managed') {
  Push-Location $repo
  try {
    & go build -o (Join-Path $bundle 'vigara-agent.exe') ./apps/agent/cmd/vigira-agent
    if ($LASTEXITCODE -ne 0) { throw 'Could not build the managed installer fixture agent' }
  } finally { Pop-Location }
  [IO.File]::WriteAllText((Join-Path $bundle 'restic.exe'),'synthetic restic, never run')
  [IO.File]::WriteAllText((Join-Path $bundle 'restic.LICENSE'),'synthetic license')
  [IO.File]::WriteAllText((Join-Path $bundle 'bootstrap.json'),'{"managedBackups":true,"shell":true,"reinstall":false}')
}
if ($Scenario -eq 'wrong-agent') { [IO.File]::WriteAllText((Join-Path $bundle 'bootstrap.json'),'{"reinstall":true,"agentId":"different-agent"}') }
if ($Scenario -in @('docker','reset-docker')) {
  Copy-Item -LiteralPath (Join-Path $repo 'apps\api\internal\agent\installers\docker_windows.ps1') -Destination (Join-Path $bundle 'configure-docker.ps1')
  if ($Scenario -eq 'docker') { [IO.File]::WriteAllText((Join-Path $bundle 'bootstrap.json'),'{"reinstall":false,"docker":{"mode":"manage","host":"http://127.0.0.1:2375"}}') }
  else {
    $data=Get-Content -Raw -LiteralPath (Join-Path $bundle 'bootstrap.json') | ConvertFrom-Json
    $data | Add-Member NoteProperty docker ([pscustomobject]@{mode='manage';host='http://127.0.0.1:2375'})
    [IO.File]::WriteAllText((Join-Path $bundle 'bootstrap.json'),($data | ConvertTo-Json))
  }
}
function FakeService($name) {
  $service = [pscustomobject]@{ Name=$name; Status='Running'; StartType='Automatic' }
  $service | Add-Member ScriptMethod WaitForStatus { param($status,$timeout) if ($this.Status -ne $status) { throw 'Unexpected service state' } }
  $service | Add-Member ScriptMethod Refresh {}
  return $service
}
$global:OldService = if ($Scenario -in @('fresh','managed','docker','updater') -or ($Scenario.StartsWith('reset') -and $Scenario -ne 'reset-legacy')) { $null } else { FakeService 'VigiraAgent' }
$global:NewService = if ($Scenario.StartsWith('reset') -and $Scenario -ne 'reset-legacy') { FakeService 'VigaraAgent' } else { $null }
$global:MigratedEnvironment = $null
function New-Object {
  $principal=[pscustomobject]@{}
  $principal | Add-Member ScriptMethod IsInRole { param($role) return $true }
  return $principal
}
function Get-Service { param($Name,$ErrorAction) if ($Name -eq 'VigiraAgent') {return $global:OldService};return $global:NewService }
function Get-CimInstance {
  param($ClassName,$Filter)
  if ($Filter -eq "Name='VigaraAgent'") {
    return [pscustomobject]@{PathName=('"'+(Join-Path $env:ProgramFiles 'Vigara\vigara-agent.exe')+'" --config "'+(Join-Path $currentDir 'agent.json')+'" run');StartName='LocalSystem'}
  }
  return [pscustomobject]@{PathName=('"'+(Join-Path $env:ProgramFiles 'Vigira\vigira-agent.exe')+'" --config "'+(Join-Path $legacyDir 'agent.json')+'" run');StartName='LocalSystem'}
}
function Get-ItemProperty { param($LiteralPath) return [pscustomobject]@{Environment=@('VIGIRA_DOCKER_ENABLED=false')} }
function New-ItemProperty { param($LiteralPath,$Name,$PropertyType,$Value,[switch]$Force) $global:MigratedEnvironment=$Value }
function Stop-Service { param($Name) if ($Name -eq 'VigiraAgent') {$global:OldService.Status='Stopped'} else {$global:NewService.Status='Stopped'} }
function Start-Service {
  param($Name)
  if ($Scenario -eq 'reset-service-failure' -and $Name -eq 'VigaraAgent') { throw 'Synthetic service startup failure' }
  if ($Name -eq 'VigiraAgent') {$global:OldService.Status='Running'} else {$global:NewService.Status='Running'}
}
function Set-Service { param($Name,$StartupType) if ($Name -eq 'VigiraAgent') {$global:OldService.StartType=$StartupType} }
function New-Service {
  param($Name,$DisplayName,$BinaryPathName,$StartupType)
  if ($Scenario -eq 'failure') { throw 'Synthetic service-creation failure' }
  $expected='"'+(Join-Path $env:ProgramFiles 'Vigara\vigara-agent.exe')+'" --config "'+(Join-Path $currentDir 'agent.json')+'" run'
  if ($Name -ne 'VigaraAgent' -or $BinaryPathName -ne $expected) {throw 'Wrong service name or quoting'}
  $global:NewService=FakeService $Name
}
function icacls.exe { $global:LASTEXITCODE=0 }
function Get-ScheduledTask { param($TaskName,$ErrorAction) return $null }
$global:UpdaterConfigured = $false
$global:UpdaterRegistered = $false
function Invoke-TestUpdater {
  if ($args[-1] -ne 'configure-updater' -or -not (Test-Path -LiteralPath $statePath)) { throw 'Updater setup lost saved enrollment' }
  $global:UpdaterConfigured = $true
  $global:LASTEXITCODE = 0
}
function New-ScheduledTaskAction { param($Execute,$Argument) return [pscustomobject]@{Execute=$Execute;Argument=$Argument} }
function New-ScheduledTaskTrigger { param([switch]$Once,$At,$RepetitionInterval) if ($RepetitionInterval.TotalMinutes -ne 1) { throw 'Updater must scan every minute' }; return [pscustomobject]@{} }
function New-ScheduledTaskPrincipal { param($UserId,$LogonType,$RunLevel) if ($UserId -ne 'SYSTEM' -or $RunLevel -ne 'Highest') { throw 'Updater principal must be LocalSystem' }; return [pscustomobject]@{} }
function New-ScheduledTaskSettingsSet { param($MultipleInstances,$ExecutionTimeLimit,[switch]$StartWhenAvailable,[switch]$AllowStartIfOnBatteries,[switch]$DontStopIfGoingOnBatteries) if ($MultipleInstances -ne 'IgnoreNew') { throw 'Updater must prevent overlap' };return [pscustomobject]@{} }
function Register-ScheduledTask {
  param($TaskName,$Action,$Trigger,$Principal,$Settings,[switch]$Force)
  if (-not $global:UpdaterConfigured -or $TaskName -ne 'VigaraClientUpdate' -or $Action.Execute -notlike '*Vigara*updater*vigara-updater.exe' -or $Action.Argument -ne 'update-client') { throw 'Updater task was not safely configured' }
  $global:UpdaterRegistered = $true
}

function Start-Sleep { param($Seconds) }
function Invoke-WebRequest { param([switch]$UseBasicParsing,$Uri,$TimeoutSec) return [pscustomobject]@{StatusCode=200;Content='OK'} }
function Read-Host {
  param($Prompt)
  if ($Scenario -eq 'reset-typo') { return 'reset' }
  if ($Prompt -like 'Type RESET*' -and $Scenario -notin @('reset-decline','reset-cancel')) { return 'RESET' }
  return ''
}
function Invoke-TestAgent {
  # Prove the old credentials/settings/queue were removed before enrollment.
  if (Test-Path -LiteralPath (Join-Path $currentDir 'agent.json')) { throw 'Old state reached enrollment' }
  $global:LASTEXITCODE = 0
  if ($Scenario -eq 'reset-failure') { $global:LASTEXITCODE = 1; return }
  [IO.File]::WriteAllText((Join-Path $currentDir 'agent.json'),'{"agentId":"production-agent","credential":"production-credential","serverUrl":"https://production.invalid"}')
}
if ($Scenario.StartsWith('reset')) {
  $invokeArgs = @{}
  if ($Scenario -in @('reset-missing','reset-reinstall','reset-noninteractive','reset-explicit','reset-cancel')) { $invokeArgs.ResetEnrollment = $true }
  if ($Scenario -eq 'reset-noninteractive') { $invokeArgs.NonInteractive = $true }
  $global:LASTEXITCODE = 0
  & (Join-Path $bundle 'install.ps1') @invokeArgs
  $failed = $Scenario -in @('reset-failure','reset-service-failure','reset-missing','reset-reinstall','reset-noninteractive','reset-cancel')
  if (($LASTEXITCODE -eq 1) -ne $failed) { throw 'Unexpected installer outcome' }
  $resetCompleted = $Scenario -in @('reset-docker','reset','reset-explicit','reset-legacy','reset-service-failure')
  $state = [IO.File]::ReadAllText((Join-Path $currentDir 'agent.json'))
  if ($resetCompleted) {
    if (($state | ConvertFrom-Json).agentId -ne 'production-agent') { throw 'New enrollment was not kept' }
    if (Test-Path -LiteralPath (Join-Path $bundle 'bootstrap.json')) { throw 'Consumed bootstrap was retained' }
  } elseif ($state -ne $saved) { throw 'Original enrollment was not preserved/restored' }
  $backups = @(Get-ChildItem -LiteralPath $currentDir -Filter 'agent.before-reset-*.json')
  if ($resetCompleted -or $Scenario -eq 'reset-failure') {
    if ($backups.Count -ne 1 -or [IO.File]::ReadAllText($backups[0].FullName) -ne $saved) { throw 'Reset backup missing or changed' }
  } elseif ($backups.Count -ne 0) { throw 'Declined/invalid reset changed state' }
  if ($Scenario -ne 'reset-service-failure' -and $global:NewService.Status -ne 'Running') { throw 'Service not running after reset/rollback' }
  if ($Scenario -eq 'reset-legacy' -and [IO.File]::ReadAllText((Join-Path $legacyDir 'agent.json')) -ne $saved) { throw 'Legacy enrollment changed' }
  if ($Scenario -eq 'reset-docker' -and $global:MigratedEnvironment -notcontains 'VIGIRA_DOCKER_MODE=manage') { throw 'RESET lost the selected Docker configuration' }
  Write-Host ('PASS: ' + $Scenario + ' installer reset behavior')
  exit 0
}
& (Join-Path $bundle 'install.ps1') -NonInteractive
if ($Scenario -in @('failure','wrong-agent')) {
  if ($LASTEXITCODE -ne 1 -or $global:NewService -or $global:OldService.Status -ne 'Running') {throw 'Failure did not preserve the original running service'}
} else {
  if (-not $global:NewService -or $global:NewService.Status -ne 'Running') {throw 'Canonical service was not started'}
  if ($global:OldService -and $global:OldService.StartType -ne 'Disabled') {throw 'Legacy service still enabled'}
  if ($Scenario -eq 'legacy' -and $global:MigratedEnvironment[0] -ne 'VIGIRA_DOCKER_ENABLED=false') {throw 'Service environment was not migrated'}
}
if ($Scenario -eq 'docker') {
  if ($global:MigratedEnvironment -notcontains 'VIGIRA_DOCKER_ENABLED=true' -or $global:MigratedEnvironment -notcontains 'VIGIRA_DOCKER_MODE=manage' -or $global:MigratedEnvironment -notcontains 'VIGIRA_DOCKER_HOST=http://127.0.0.1:2375') { throw 'Docker settings were not installed on the service' }
}
if ($Scenario -eq 'managed') {
  $state=Get-Content -Raw -LiteralPath (Join-Path $currentDir 'agent.json') | ConvertFrom-Json
  if (-not $state.managedBackups -or $state.agentId -ne 'synthetic-agent' -or $state.credential -ne 'synthetic-credential') {throw 'Managed opt-in did not preserve enrollment'}
  if (-not $state.shellEnabled) { throw 'Installer did not enable device shell access' }
  if (-not (Test-Path -LiteralPath (Join-Path $env:ProgramFiles 'Vigara\restic.exe'))) {throw 'Restic was not installed'}
} elseif ([IO.File]::ReadAllText((Join-Path $currentDir 'agent.json')) -ne $saved) {throw 'Enrollment changed during migration'}
if ($Scenario -notin @('fresh','managed','docker','updater') -and [IO.File]::ReadAllText((Join-Path $legacyDir 'agent.json')) -ne $saved) {throw 'Legacy credentials were modified'}
if ($Scenario -eq 'updater' -and (-not $global:UpdaterRegistered -or -not $global:UpdaterConfigured)) { throw 'Installer omitted self-update setup' }
Write-Host ('PASS: ' + $Scenario + ' installer migration behavior')
