package org.vigara.mobile.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
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
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.Candidate
import org.vigara.mobile.data.CreatedStack
import org.vigara.mobile.data.Discovery
import org.vigara.mobile.data.GitSettings
import org.vigara.mobile.data.ImportPreview
import org.vigara.mobile.data.MessageResult
import org.vigara.mobile.data.SetupDevices
import org.vigara.mobile.data.User
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ConfirmDialog
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.ItemCard
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.MonoBlock
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.rememberDownloader
import org.vigara.mobile.ui.common.rememberLoad

@Composable
fun StackImportScreen(api: VigaraApi, onBack: () -> Unit, onImported: (String) -> Unit) {
    val devices = rememberLoad { api.get<SetupDevices>("/api/stacks/setup").devices }
    var agent by remember { mutableStateOf<String?>(null) }
    var discovery by remember { mutableStateOf<Discovery?>(null) }
    var preview by remember { mutableStateOf<ImportPreview?>(null) }
    var scan by remember { mutableStateOf(0) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var name by remember { mutableStateOf("") }
    var id by remember { mutableStateOf("") }
    var reviewed by remember { mutableStateOf(false) }
    var recoverySaved by remember { mutableStateOf(false) }
    var pendingId by remember { mutableStateOf<String?>(null) }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    val download = rememberDownloader(api) { scope.launch { snackbar.showSnackbar(it) } }

    val list = devices.data.orEmpty()
    LaunchedEffect(list) { if (agent == null) agent = list.firstOrNull { it.managedRoot.isNotEmpty() }?.agentId }
    LaunchedEffect(agent, scan) {
        val a = agent ?: return@LaunchedEffect
        busy = true; error = null; discovery = null
        try { discovery = api.post("/api/stacks/imports/discover", buildJsonObject { put("agentId", a) }) } catch (e: ApiException) { error = e.message }
        busy = false
    }

    AppScaffold("Import stack", onBack = { if (preview != null) preview = null else onBack() }, snackbar = snackbar) { padding ->
        Column(Modifier.padding(padding).verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            InfoBanner("Adopt an existing Compose project into Git without restarting its containers. Its project name, folder, networks and data mounts stay in place; deployment is a separate action.")
            error?.let { ErrorBanner(it) }
            pendingId?.let { p ->
                InfoBanner("Imported, but the Git save needs a retry. The encrypted draft is safe in Vigara; open it and save again after fixing the repository connection. No containers were restarted.")
                Button({ onImported(p) }) { Text("Open imported draft") }
            }
            if (list.isNotEmpty() && preview == null) {
                Text("Device", style = MaterialTheme.typography.labelMedium)
                list.forEach { d -> FilterChip(agent == d.agentId, { agent = d.agentId }, enabled = d.managedRoot.isNotEmpty(), label = { Text(d.name + if (d.managedRoot.isEmpty()) " — enable deployment" else "") }) }
            }
            if (devices.data != null && list.none { it.managedRoot.isNotEmpty() }) InfoBanner("Enroll the host with Docker deployment enabled, or reinstall its agent. Import only scans the host's configured Compose root.")
            if (busy && preview == null) Spinner()

            val d = discovery
            if (d != null && preview == null) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Compose root: ${d.root}", Modifier.weight(1f), style = MaterialTheme.typography.bodySmall)
                    TextButton({ scan++ }, enabled = !busy) { Text("Rescan") }
                }
                if (d.candidates.isEmpty()) Text("No existing Compose projects matched Docker's containers in this root.", color = MaterialTheme.colorScheme.onSurfaceVariant)
                d.candidates.forEach { c -> CandidateCard(c, busy) {
                    scope.launch {
                        busy = true; error = null; reviewed = false; recoverySaved = false
                        try {
                            preview = api.post("/api/stacks/imports/preview", buildJsonObject { put("agentId", agent.orEmpty()); put("directory", c.directory); put("token", c.token) })
                            name = c.projectName
                            id = c.directory.substringAfterLast('/').lowercase().replace(Regex("[^a-z0-9]+"), "-").trim('-').take(63)
                        } catch (e: ApiException) { error = e.message }
                        busy = false
                    }
                } }
            }

            val p = preview
            if (p != null && pendingId == null) {
                Text("Keep project name: ${p.candidate.projectName}", style = MaterialTheme.typography.titleSmall)
                p.candidate.storage.forEach { Text(it, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace) }
                p.candidate.issues.forEach { InfoBanner(it) }
                OutlinedTextField(name, { name = it }, label = { Text("Stack name") }, singleLine = true, enabled = !busy, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(id, { id = it }, label = { Text("Stack ID") }, supportingText = { Text("A new ID for this deployment; the Docker project name stays the same.") }, singleLine = true, enabled = !busy, modifier = Modifier.fillMaxWidth())
                p.files.forEach { (file, content) -> SectionTitle(file); MonoBlock(content) }
                Text("Environment files are encrypted before saving to Git: ${p.candidate.environmentFiles.distinct().joinToString(", ").ifEmpty { "none" }}. Their contents are not shown.", style = MaterialTheme.typography.bodySmall)
                Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(reviewed, { reviewed = it }); Text("I reviewed the Compose files for inline secrets and checked the storage paths.", style = MaterialTheme.typography.bodySmall) }
                OutlinedButton({ download("GET", "/api/stacks/imports/recovery", "vigara-recovery-key.txt", null, null) }) { Text("Download recovery key") }
                Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(recoverySaved, { recoverySaved = it }); Text("I saved the recovery key securely outside this server.", style = MaterialTheme.typography.bodySmall) }
                Button(enabled = !busy && reviewed && recoverySaved && name.isNotBlank() && Regex("^[a-z0-9]+(?:-[a-z0-9]+)*$").matches(id), onClick = {
                    scope.launch {
                        busy = true; error = null
                        try {
                            val r = api.post<CreatedStack>("/api/stacks/imports", buildJsonObject {
                                put("agentId", agent.orEmpty()); put("directory", p.candidate.directory); put("token", p.candidate.token)
                                put("id", id); put("name", name.trim()); put("reviewed", reviewed); put("recoverySaved", recoverySaved)
                            })
                            if (r.state == "saved") onImported(r.id) else pendingId = r.id
                        } catch (e: ApiException) { error = e.message }
                        busy = false
                    }
                }) { if (busy) Spinner() else Text("Import and save to Git") }
            }
        }
    }
}

@Composable
private fun CandidateCard(c: Candidate, busy: Boolean, onReview: () -> Unit) {
    ItemCard {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(c.projectName, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
            StatusChip(if (c.imported) "Already registered" else c.state, if (c.imported) Palette.idle else Palette.info)
        }
        Text("${c.directory} · ${c.containers} container(s) · ${c.composeFiles.joinToString(", ")}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        c.issues.forEach { Text("• $it", style = MaterialTheme.typography.bodySmall, color = Palette.warn) }
        if (!c.imported) TextButton(onReview, enabled = !busy && c.issues.isEmpty()) { Text("Review and import") }
    }
}

@Composable
fun GitSettingsScreen(api: VigaraApi, user: User, onBack: () -> Unit, onWeb: (String, WebTarget) -> Unit) {
    val settings = rememberLoad { api.get<GitSettings>("/api/stacks/settings") }
    var remote by remember { mutableStateOf<String?>(null) }
    var branch by remember { mutableStateOf<String?>(null) }
    var mode by remember { mutableStateOf<String?>(null) }
    var orgName by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var notice by remember { mutableStateOf<String?>(null) }
    var disconnecting by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    AppScaffold("Git settings", onBack = onBack) { padding ->
        Column(Modifier.padding(padding)) {
            LoadedContent(settings) { s ->
                val r = remote ?: s.remote; val b = branch ?: s.branch; val m = mode ?: s.authMode
                val github = s.githubApp
                val appMode = m == "github-app"
                val ready = r.isNotBlank() && b.isNotBlank() && (!appMode || github.connected)
                fun submit(save: Boolean) {
                    scope.launch {
                        busy = true; error = null; notice = null
                        try {
                            val body = buildJsonObject { put("remote", r.trim()); put("branch", b.trim()); put("version", s.version); put("authMode", m) }
                            notice = (if (save) api.put<MessageResult>("/api/stacks/settings", body) else api.post<MessageResult>("/api/stacks/settings/test", body)).message.ifBlank { if (save) "Saved." else "Connection works." }
                            if (save) settings.refresh()
                        } catch (e: ApiException) { error = e.message }
                        busy = false
                    }
                }
                Column(Modifier.verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (!user.isAdmin) InfoBanner("Only administrators can change these settings.")
                    error?.let { ErrorBanner(it) }
                    notice?.let { InfoBanner(it) }
                    Text("All stacks use this repository and branch. Each save commits and pushes; deployment is a separate action.", style = MaterialTheme.typography.bodySmall)
                    OutlinedTextField(r, { remote = it; notice = null }, label = { Text("Repository URL") }, placeholder = { Text("https://github.com/owner/stacks.git") }, singleLine = true, enabled = !busy && user.isAdmin, modifier = Modifier.fillMaxWidth())
                    OutlinedTextField(b, { branch = it; notice = null }, label = { Text("Branch") }, singleLine = true, enabled = !busy && user.isAdmin, modifier = Modifier.fillMaxWidth())
                    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        FilterChip(m == "github-app", { mode = "github-app" }, label = { Text("GitHub App") }, enabled = user.isAdmin)
                        FilterChip(m == "server", { mode = "server" }, label = { Text("Server credentials") }, enabled = user.isAdmin)
                    }
                    if (appMode) {
                        Text(if (github.connected) "Connected: ${github.slug}" else "Connect a GitHub App", style = MaterialTheme.typography.titleSmall)
                        Text("Vigara renews short-lived credentials automatically. GitHub asks you to create an App and pick the repository it can access; this opens inside the app.", style = MaterialTheme.typography.bodySmall)
                        if (!github.available && github.setupHelp.isNotBlank()) InfoBanner(github.setupHelp)
                        if (!github.connected && github.canManage) {
                            OutlinedTextField(orgName, { orgName = it }, label = { Text("App owner organization (optional)") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                            if (github.pending) OutlinedButton({
                                scope.launch {
                                    busy = true; error = null
                                    try {
                                        val res = api.post<kotlinx.serialization.json.JsonObject>("/api/stacks/settings/github/install")
                                        onWeb("Resume GitHub App", WebTarget.Url((res["url"] as? kotlinx.serialization.json.JsonPrimitive)?.content.orEmpty()))
                                    } catch (e: ApiException) { error = e.message }
                                    busy = false
                                }
                            }, enabled = !busy && github.available) { Text("Resume App installation") }
                            Button({
                                scope.launch {
                                    busy = true; error = null
                                    try {
                                        val start = api.post<kotlinx.serialization.json.JsonObject>("/api/stacks/settings/github/start", buildJsonObject { put("remote", r.trim()); put("branch", b.trim()); put("organization", orgName.trim()) })
                                        val action = (start["action"] as? kotlinx.serialization.json.JsonPrimitive)?.content.orEmpty()
                                        val manifest = start["manifest"]?.toString().orEmpty()
                                        onWeb("Connect GitHub", WebTarget.PostForm(action, "manifest", manifest))
                                    } catch (e: ApiException) { error = e.message }
                                    busy = false
                                }
                            }, enabled = !busy && github.available && r.isNotBlank() && b.isNotBlank()) { Text(if (github.pending) "Start a new App registration" else "Connect with GitHub") }
                        }
                        if (github.connected && github.canManage) OutlinedButton({ disconnecting = true }) { Text("Disconnect App") }
                    } else Text("Git uses the Vigara service account's SSH keys or credential helper, configured on the server.", style = MaterialTheme.typography.bodySmall)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedButton({ submit(false) }, enabled = ready && !busy && user.isAdmin) { Text("Test connection") }
                        Button({ submit(true) }, enabled = ready && !busy && user.isAdmin) { if (busy) Spinner() else Text("Save settings") }
                    }
                }
                if (disconnecting) ConfirmDialog("Disconnect this App?", "GitHub App authentication stops working in Vigara. The repository and branch stay configured. Revoke the installation on GitHub separately.", "Disconnect", destructive = true,
                    onConfirm = {
                        disconnecting = false
                        scope.launch {
                            try { notice = api.send("DELETE", "/api/stacks/settings/github").let { "App disconnected." }; settings.refresh() } catch (e: ApiException) { error = e.message }
                        }
                    }, onDismiss = { disconnecting = false })
            }
        }
    }
}
