$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot '../apps/api/internal/agent/installers/docker_windows.ps1')
foreach ($mode in @('read','manage','exec')) {
  $settings = [pscustomobject]@{mode=$mode;host='http://127.0.0.1:2375'}
  $environment = @(Get-DockerEnvironment $settings)
  if ($environment -notcontains "VIGIRA_DOCKER_MODE=$mode" -or $environment -notcontains 'VIGIRA_DOCKER_ENABLED=true') { throw 'Incorrect service environment' }
}
if (@(Get-DockerEnvironment $null).Count -ne 0) { throw 'Omitted settings must not change the service environment' }
foreach ($settings in @(
  @{mode='deploy';host='http://127.0.0.1:2375'},
  @{mode='read';host='http://192.168.1.10:2375'},
  @{mode='read';host='npipe:////./pipe/docker_engine'},
  @{mode='read';host='http://127.0.0.1'},
  @{mode='read';host="http://127.0.0.1:2375`nUser=root"},
  @{mode='read';host='http://127.0.0.1:2375';managedRoot='/srv/stacks'}
)) {
  $rejected = $false
  try { Get-DockerEnvironment ([pscustomobject]$settings) | Out-Null } catch { $rejected = $true }
  if (-not $rejected) { throw 'Accepted unsupported Docker settings' }
}
Write-Host 'PASS: Windows Docker opt-in, endpoint validation and service environment'
