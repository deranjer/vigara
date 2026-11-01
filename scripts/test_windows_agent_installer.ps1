param([ValidateSet('legacy','fresh','failure','wrong-agent','managed')][string]$Scenario = 'legacy')
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
if ($Scenario -in @('fresh','managed')) { [IO.File]::WriteAllText((Join-Path $currentDir 'agent.json'),$saved) }
else { [IO.File]::WriteAllText((Join-Path $legacyDir 'agent.json'),$saved) }
$bundle = Join-Path $fixture 'bundle'
New-Item -ItemType Directory -Force $bundle | Out-Null
Copy-Item -LiteralPath (Join-Path $repo 'apps\api\internal\agent\installers\windows.ps1') -Destination (Join-Path $bundle 'install.ps1')
if ($Scenario -ne 'managed') { [IO.File]::WriteAllText((Join-Path $bundle 'vigara-agent.exe'),'synthetic executable, never run') }
if ($Scenario -eq 'managed') {
  Push-Location $repo
  try {
    & go build -o (Join-Path $bundle 'vigara-agent.exe') ./apps/agent/cmd/vigira-agent
    if ($LASTEXITCODE -ne 0) { throw 'Could not build the managed installer fixture agent' }
  } finally { Pop-Location }
  [IO.File]::WriteAllText((Join-Path $bundle 'restic.exe'),'synthetic restic, never run')
  [IO.File]::WriteAllText((Join-Path $bundle 'restic.LICENSE'),'synthetic license')
  [IO.File]::WriteAllText((Join-Path $bundle 'bootstrap.json'),'{"managedBackups":true,"reinstall":false}')
}
if ($Scenario -eq 'wrong-agent') { [IO.File]::WriteAllText((Join-Path $bundle 'bootstrap.json'),'{"reinstall":true,"agentId":"different-agent"}') }
function FakeService($name) {
  $service = [pscustomobject]@{ Name=$name; Status='Running'; StartType='Automatic' }
  $service | Add-Member ScriptMethod WaitForStatus { param($status,$timeout) if ($this.Status -ne $status) { throw 'Unexpected service state' } }
  $service | Add-Member ScriptMethod Refresh {}
  return $service
}
$global:OldService = if ($Scenario -in @('fresh','managed')) { $null } else { FakeService 'VigiraAgent' }
$global:NewService = $null
$global:MigratedEnvironment = $null
function New-Object {
  $principal=[pscustomobject]@{}
  $principal | Add-Member ScriptMethod IsInRole { param($role) return $true }
  return $principal
}
function Get-Service { param($Name,$ErrorAction) if ($Name -eq 'VigiraAgent') {return $global:OldService};return $global:NewService }
function Get-CimInstance {
  param($ClassName,$Filter)
  return [pscustomobject]@{PathName=('"'+(Join-Path $env:ProgramFiles 'Vigira\vigira-agent.exe')+'" --config "'+(Join-Path $legacyDir 'agent.json')+'" run');StartName='LocalSystem'}
}
function Get-ItemProperty { param($LiteralPath) return [pscustomobject]@{Environment=@('VIGIRA_DOCKER_ENABLED=false')} }
function New-ItemProperty { param($LiteralPath,$Name,$PropertyType,$Value,[switch]$Force) $global:MigratedEnvironment=$Value }
function Stop-Service { param($Name) if ($Name -eq 'VigiraAgent') {$global:OldService.Status='Stopped'} else {$global:NewService.Status='Stopped'} }
function Start-Service { param($Name) if ($Name -eq 'VigiraAgent') {$global:OldService.Status='Running'} else {$global:NewService.Status='Running'} }
function Set-Service { param($Name,$StartupType) if ($Name -eq 'VigiraAgent') {$global:OldService.StartType=$StartupType} }
function New-Service {
  param($Name,$DisplayName,$BinaryPathName,$StartupType)
  if ($Scenario -eq 'failure') { throw 'Synthetic service-creation failure' }
  $expected='"'+(Join-Path $env:ProgramFiles 'Vigara\vigara-agent.exe')+'" --config "'+(Join-Path $currentDir 'agent.json')+'" run'
  if ($Name -ne 'VigaraAgent' -or $BinaryPathName -ne $expected) {throw 'Wrong service name or quoting'}
  $global:NewService=FakeService $Name
}
function icacls.exe { $global:LASTEXITCODE=0 }
function Start-Sleep { param($Seconds) }
& (Join-Path $bundle 'install.ps1') -NonInteractive
if ($Scenario -in @('failure','wrong-agent')) {
  if ($LASTEXITCODE -ne 1 -or $global:NewService -or $global:OldService.Status -ne 'Running') {throw 'Failure did not preserve the original running service'}
} else {
  if (-not $global:NewService -or $global:NewService.Status -ne 'Running') {throw 'Canonical service was not started'}
  if ($global:OldService -and $global:OldService.StartType -ne 'Disabled') {throw 'Legacy service still enabled'}
  if ($Scenario -eq 'legacy' -and $global:MigratedEnvironment[0] -ne 'VIGIRA_DOCKER_ENABLED=false') {throw 'Service environment was not migrated'}
}
if ($Scenario -eq 'managed') {
  $state=Get-Content -Raw -LiteralPath (Join-Path $currentDir 'agent.json') | ConvertFrom-Json
  if (-not $state.managedBackups -or $state.agentId -ne 'synthetic-agent' -or $state.credential -ne 'synthetic-credential') {throw 'Managed opt-in did not preserve enrollment'}
  if (-not (Test-Path -LiteralPath (Join-Path $env:ProgramFiles 'Vigara\restic.exe'))) {throw 'Restic was not installed'}
} elseif ([IO.File]::ReadAllText((Join-Path $currentDir 'agent.json')) -ne $saved) {throw 'Enrollment changed during migration'}
if ($Scenario -notin @('fresh','managed') -and [IO.File]::ReadAllText((Join-Path $legacyDir 'agent.json')) -ne $saved) {throw 'Legacy credentials were modified'}
Write-Host ('PASS: ' + $Scenario + ' installer migration behavior')
