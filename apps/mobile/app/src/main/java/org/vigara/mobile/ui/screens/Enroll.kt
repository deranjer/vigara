package org.vigara.mobile.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.DownloadAvailability
import org.vigara.mobile.data.EnrollmentStatus
import org.vigara.mobile.data.McpStatus
import org.vigara.mobile.data.User
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.MonoBlock
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.rememberDownloader
import org.vigara.mobile.ui.common.rememberLoad

private val targetLabels = mapOf("windows-amd64" to "Windows · x64", "linux-amd64" to "Linux · x64", "linux-arm64" to "Linux · ARM64")
private val dockerLevels = listOf("read" to "View only", "manage" to "Manage", "exec" to "Manage + terminals", "deploy" to "Full control (deploy stacks)")
private val dockerFeatures = mapOf(
    "read" to emptyList(), "manage" to listOf("docker.manage"), "exec" to listOf("docker.manage", "docker.exec"),
    "deploy" to listOf("docker.manage", "docker.exec", "docker.deploy"),
)

/**
 * Creates a one-use agent installer bundle and saves it where you choose (for example to copy to the
 * new computer), then follows the enrollment until the device checks in.
 */
@Composable
fun AddDeviceScreen(api: VigaraApi, user: User, onBack: () -> Unit) {
    val availability = rememberLoad(pollMs = 8_000) { api.get<DownloadAvailability>("/api/agent-downloads/availability") }
    var serverUrl by remember { mutableStateOf<String?>(null) }
    var target by remember { mutableStateOf<String?>(null) }
    var name by remember { mutableStateOf("") }
    var shell by remember { mutableStateOf(false) }
    var docker by remember { mutableStateOf(false) }
    var level by remember { mutableStateOf("read") }
    var dockerHost by remember { mutableStateOf("") }
    var composeRoot by remember { mutableStateOf("") }
    var stackOwner by remember { mutableStateOf("") }
    var session by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    var message by remember { mutableStateOf<String?>(null) }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    val download = rememberDownloader(api) { message = it }

    val av = availability.data
    LaunchedEffect(av) {
        if (av != null) {
            if (serverUrl == null || av.serverUrls.none { it.url == serverUrl }) serverUrl = av.serverUrl.ifEmpty { av.serverUrls.firstOrNull()?.url }
            if (target == null || target !in av.targets) target = av.targets.firstOrNull()
        }
    }
    LaunchedEffect(session, status) {
        if (session.isEmpty()) return@LaunchedEffect
        while (status !in setOf("enrolled", "canceled", "expired")) {
            try { status = api.get<EnrollmentStatus>("/api/agent-downloads/$session").status } catch (_: ApiException) {}
            delay(3000)
        }
    }

    val linux = target?.startsWith("linux-") == true
    val chosen = av?.serverUrls?.firstOrNull { it.url == serverUrl }
    val ownerValid = stackOwner.isBlank() || Regex("^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63})?$").matches(stackOwner.trim())

    AppScaffold("Add device", onBack = onBack, snackbar = snackbar) { padding ->
        LoadedContent(availability) { a ->
            Column(Modifier.verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (!user.isAdmin) { InfoBanner("Only administrators can enroll devices."); return@Column }
                message?.let { InfoBanner(it) }
                if (a.serverUrls.isEmpty()) InfoBanner("No server URL is configured. Set VIGIRA_PUBLIC_URL to the HTTPS address agents can reach (or allow insecure HTTP on a trusted network) and restart the server.")
                if (a.targets.isEmpty()) InfoBanner("Agent bundles are not ready yet. The server is downloading and verifying agent releases; this list refreshes automatically.")
                Text("Server address the agent will connect to", style = MaterialTheme.typography.labelMedium)
                a.serverUrls.forEach { o -> FilterChip(serverUrl == o.url, { serverUrl = o.url }, enabled = session.isEmpty(), label = { Text(if (o.secure) o.url else "${o.url} (HTTP)") }) }
                if (chosen != null && !chosen.secure) InfoBanner("This connection is not encrypted. The agent's credential and telemetry can be read by anyone on the network path. Use only on a trusted private network or VPN.")
                Text("Operating system", style = MaterialTheme.typography.labelMedium)
                targetLabels.forEach { (t, l) -> FilterChip(target == t, { target = t }, enabled = t in a.targets && session.isEmpty(), label = { Text(l) }) }
                OutlinedTextField(name, { name = it }, label = { Text("Device name (unique)") }, singleLine = true, enabled = session.isEmpty(), modifier = Modifier.fillMaxWidth())
                Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(shell, { shell = it }, enabled = session.isEmpty()); Text("Allow a device shell (runs as the agent account)", style = MaterialTheme.typography.bodySmall) }
                Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(docker, { docker = it }, enabled = session.isEmpty()); Text("Enable Docker access", style = MaterialTheme.typography.bodySmall) }
                if (docker) {
                    dockerLevels.forEach { (v, l) -> FilterChip(level == v, { level = v }, enabled = session.isEmpty() && (v != "deploy" || linux), label = { Text(l) }) }
                    OutlinedTextField(dockerHost, { dockerHost = it }, label = { Text("Docker endpoint (optional)") }, placeholder = { Text(if (linux) "unix:///var/run/docker.sock" else "http://127.0.0.1:2375") }, singleLine = true, enabled = session.isEmpty(), modifier = Modifier.fillMaxWidth())
                    if (level == "deploy") {
                        OutlinedTextField(composeRoot, { composeRoot = it }, label = { Text("Managed Compose directory") }, placeholder = { Text("/var/lib/vigara-agent/compose-stacks") }, singleLine = true, enabled = session.isEmpty(), modifier = Modifier.fillMaxWidth())
                        OutlinedTextField(stackOwner, { stackOwner = it }, label = { Text("Stack folder owner (user[:group])") }, singleLine = true, isError = !ownerValid, enabled = session.isEmpty(), modifier = Modifier.fillMaxWidth())
                    }
                    InfoBanner("Docker socket access can control the host, even when Vigara is set to view-only. Linux stack deployment runs the agent as root with write access to the Compose directory. Mounted-file access and managed backups can be enabled by reinstalling from the web UI.")
                }
                if (session.isEmpty()) {
                    Button(enabled = chosen != null && target != null && name.isNotBlank() && ownerValid && target in a.targets, onClick = {
                        message = null
                        val body = buildJsonObject {
                            if (shell) put("shell", true) else put("shell", false)
                            put("target", target.orEmpty()); put("serverUrl", serverUrl.orEmpty()); put("name", name.trim())
                            if (docker) {
                                put("docker", buildJsonObject {
                                    put("mode", level)
                                    put("host", dockerHost.trim().ifEmpty { if (linux) "unix:///var/run/docker.sock" else "http://127.0.0.1:2375" })
                                    if (level == "deploy") { put("managedRoot", composeRoot.trim()); if (stackOwner.isNotBlank()) put("stackOwner", stackOwner.trim()) }
                                    put("files", "disabled")
                                })
                            }
                            put("features", JsonArray((if (docker) dockerFeatures[level].orEmpty() else emptyList()).map { JsonPrimitive(it) }))
                        }
                        download("POST", "/api/agent-downloads", "vigara-agent-$target.zip", body) { response ->
                            val id = response.header("X-Vigara-Session-ID") ?: response.header("X-Vigira-Session-ID").orEmpty()
                            session = id; status = "waiting"
                        }
                    }) { Text("Create and save installer") }
                    Text("The installer is a zip you copy to the new device. It contains a one-use enrollment that expires after 30 minutes.", style = MaterialTheme.typography.bodySmall)
                } else {
                    SectionTitle("Enrollment")
                    StatusChip(status.ifBlank { "waiting" }, when (status) { "enrolled" -> Palette.good; "waiting" -> Palette.info; else -> Palette.idle })
                    when (status) {
                        "enrolled" -> Text("The device enrolled. Installation is complete once the installer confirms the service is running and the device reports in.")
                        "waiting", "" -> {
                            Text("Extract the bundle on the device and run ${if (linux) "sh install.sh" else "install.cmd"} as an administrator.")
                            OutlinedButton({
                                scope.launch {
                                    try { api.postUnit("/api/agent-downloads/$session/cancel"); status = "canceled" } catch (e: ApiException) { snackbar.showSnackbar("This enrollment has already finished or expired.") }
                                }
                            }) { Text("Cancel enrollment") }
                        }
                        else -> Text("This enrollment is $status.")
                    }
                    if (status in setOf("enrolled", "canceled", "expired")) TextButton({ session = ""; status = ""; name = "" }) { Text("Add another") }
                }
            }
        }
    }
}

@Composable
fun McpScreen(api: VigaraApi, user: User, onBack: () -> Unit, onManage: () -> Unit) {
    val status = rememberLoad { api.get<McpStatus>("/api/mcp/status") }
    val clipboard = LocalClipboardManager.current
    AppScaffold("Claude and ChatGPT (MCP)", onBack = onBack) { padding ->
        Column(Modifier.padding(padding)) {
            LoadedContent(status) { s ->
                val origin = s.serverUrl.removeSuffix("/mcp").ifEmpty { "https://vigara.example.com" }
                Column(Modifier.verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    StatusChip(if (s.enabled) "MCP enabled" else "MCP disabled", if (s.enabled) Palette.good else Palette.idle)
                    Text("Connect an AI assistant to read device health, containers, backup reports and alerts. Sign in with your Vigara account through OAuth; no OpenAI or Anthropic API key is needed. Access is read-only and limited by your account.", style = MaterialTheme.typography.bodyMedium)
                    if (s.enabled && s.serverUrl.isNotEmpty()) {
                        SectionTitle("Remote MCP server URL")
                        MonoBlock(s.serverUrl)
                        OutlinedButton({ clipboard.setText(AnnotatedString(s.serverUrl)) }) { Text("Copy URL") }
                        Text("Your AI provider must be able to reach this HTTPS address; a private LAN alone is not enough for cloud connectors.", style = MaterialTheme.typography.bodySmall)
                        Button(onManage) { Text("Manage connected applications") }
                        Text("Disconnect applications to revoke access. Password changes also revoke them.", style = MaterialTheme.typography.bodySmall)
                    } else {
                        InfoBanner(if (user.isAdmin) "Enable MCP by setting the variables below on the API server, then recreate its container." else "Ask your server administrator to enable MCP.")
                        SectionTitle("Server setup")
                        MonoBlock("VIGIRA_MCP_URL=$origin\nVIGIRA_COOKIE_SECURE=true")
                        Text("Use your public HTTPS origin without /mcp. Route /mcp, /oauth/* and /.well-known/oauth-* to Vigara, preserving Host, with no extra proxy login on those routes.", style = MaterialTheme.typography.bodySmall)
                    }
                    SectionTitle("Connect ChatGPT")
                    Text("1. In ChatGPT's web app, open Plugins and add a custom MCP server.\n2. Paste the MCP URL, choose OAuth with dynamic client registration, and leave client ID and secret empty.\n3. Sign in on Vigara's consent page, approve, then select the plugin with @ in a chat.", style = MaterialTheme.typography.bodySmall)
                    SectionTitle("Connect Claude")
                    Text("1. Open Customize → Connectors → Add custom connector.\n2. Paste the MCP URL, choose OAuth and Register automatically.\n3. Connect, sign in with Vigara and approve access, then enable it for your conversation.", style = MaterialTheme.typography.bodySmall)
                    Text("Try: \"Use Vigara to list my devices and any failing backups.\"", style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
}
