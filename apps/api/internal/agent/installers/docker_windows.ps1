function Get-DockerEnvironment($settings) {
  if ($null -eq $settings) { return @() }
  if (($settings.PSObject.Properties.Name -contains 'files') -and $settings.files -and $settings.files -ne 'disabled') { throw 'Mounted file access requires a native Linux Docker host.' }
  if ($settings.mode -notin @('read', 'manage', 'exec')) { throw 'Windows Docker access must be read, manage, or exec. Compose deployment is Linux-only.' }
  $uri = $null
  $ip = $null
  if (-not [Uri]::TryCreate([string]$settings.host, [UriKind]::Absolute, [ref]$uri) -or
      $uri.Scheme -ne 'http' -or $uri.UserInfo -or $uri.Query -or $uri.Fragment -or
      $uri.AbsolutePath -ne '/' -or ([string]$settings.host).EndsWith('/') -or
      ([string]$settings.host) -notmatch ':\d+$' -or
      -not [Net.IPAddress]::TryParse($uri.DnsSafeHost.Trim('[',']'), [ref]$ip) -or
      -not [Net.IPAddress]::IsLoopback($ip)) {
    throw 'Windows Docker access requires an existing loopback HTTP IP endpoint; named pipes and remote endpoints are unsupported.'
  }
  if (($settings.PSObject.Properties.Name -contains 'managedRoot') -and $settings.managedRoot) { throw 'A Compose root is supported only on Linux with deploy access.' }
  return @('VIGIRA_DOCKER_ENABLED=true', ('VIGIRA_DOCKER_HOST=' + $settings.host), ('VIGIRA_DOCKER_MODE=' + $settings.mode))
}

function Test-DockerEndpoint($settings) {
  $response = Invoke-WebRequest -UseBasicParsing -Uri ($settings.host + '/_ping') -TimeoutSec 10
  if ($response.StatusCode -ne 200 -or ([string]$response.Content).Trim() -ne 'OK') {
    throw 'Docker did not answer its health check. Start the selected local endpoint and rerun the installer.'
  }
}
