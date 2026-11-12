package org.vigara.mobile.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.PrimaryScrollableTabRow
import androidx.compose.runtime.LaunchedEffect
import org.vigara.mobile.data.StackListing
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.Fleet
import org.vigara.mobile.data.HostEntry
import org.vigara.mobile.data.MessageResult
import org.vigara.mobile.data.StackDeployment
import org.vigara.mobile.data.StackDetail
import org.vigara.mobile.data.StackList
import org.vigara.mobile.data.StackPlan
import org.vigara.mobile.data.StackRevision
import org.vigara.mobile.data.StackSummary
import org.vigara.mobile.data.User
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ConfirmDialog
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.ItemCard
import org.vigara.mobile.ui.common.KeyValue
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.MonoBlock
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.formatTime
import org.vigara.mobile.ui.common.rememberLoad
import android.net.Uri

private fun stackStatus(s: StackSummary): Pair<String, androidx.compose.ui.graphics.Color> = when {
    s.commit.isBlank() -> "Draft" to Palette.idle
    !s.valid -> "Needs review" to Palette.warn
    !s.assigned -> "No target" to Palette.idle
    s.deploymentStatus == "queued" || s.deploymentStatus == "running" -> "Deploying" to Palette.warn
    s.deploymentStatus == "failed" -> "Deployment failed" to Palette.bad
    s.deploymentStatus == "unknown" -> "Check deployment" to Palette.warn
    s.deploymentStatus == "succeeded" -> {
        val same = if (s.digest.isNotBlank() && s.deploymentDigest.isNotBlank()) s.digest == s.deploymentDigest else s.deploymentCommit == s.commit
        if (same) "Deployed" to Palette.good else "Saved changes" to Palette.info
    }
    else -> (if (s.imported) "Imported" else "Not deployed") to Palette.idle
}

private fun runtimeOf(stack: StackSummary, hosts: List<HostEntry>): Triple<Int, Int, Boolean>? {
    val host = hosts.firstOrNull { it.agentId == stack.agentId }?.takeIf { it.state == "ok" } ?: return null
    val ours = host.containers.filter { it.project == stack.projectName }
    return Triple(ours.count { it.state == "running" }, ours.size, host.truncated)
}

@Composable
fun StacksScreen(api: VigaraApi, user: User, onOpen: (String) -> Unit, onImport: () -> Unit, onGit: () -> Unit) {
    val stacks = rememberLoad(pollMs = 15_000) { api.get<StackListing>("/api/stacks") to api.get<Fleet>("/api/containers").hosts }
    var search by remember { mutableStateOf("") }
    var creating by remember { mutableStateOf(false) }
    AppScaffold("Stacks", actions = {
        if (user.canOperate) TextButton({ creating = true }) { Text("New") }
        if (user.canOperate) TextButton(onImport) { Text("Import") }
        if (user.isAdmin) TextButton(onGit) { Text("Git") }
    }) { padding ->
        Column(Modifier.padding(padding)) {
            OutlinedTextField(search, { search = it }, singleLine = true, placeholder = { Text("Search stacks") }, modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp))
            LoadedContent(stacks) { (listing, hosts) ->
                val list = listing.stacks
                val needle = search.trim().lowercase()
                val shown = list.filter { needle.isEmpty() || listOf(it.id, it.name, it.projectName, it.deviceName).any { f -> f.lowercase().contains(needle) } }
                    .sortedBy { it.name.ifBlank { it.id }.lowercase() }
                if (!listing.configured) EmptyState("Stack storage is not configured yet. Admins can set up the Git repository from the Git button.")
                else if (shown.isEmpty()) EmptyState(if (list.isEmpty()) "No stacks yet. Use New to create one or Import to adopt an existing Compose project." else "No stacks match.")
                else LazyColumn(contentPadding = PaddingValues(bottom = 16.dp)) {
                    items(shown, key = { it.id }) { stack ->
                        val (label, color) = stackStatus(stack)
                        val runtime = runtimeOf(stack, hosts)
                        org.vigara.mobile.ui.common.TwoLineRow(
                            title = stack.name.ifBlank { stack.id },
                            subtitle = "$label · ${stack.deviceName.ifBlank { stack.agentId.ifBlank { "no device assigned" } }}",
                            dot = color,
                            trailing = runtime?.let { (running, total, partial) -> "${if (partial) "≥" else ""}$running/$total" },
                            trailingDetail = runtime?.let { "running" },
                            trailingColor = runtime?.let { (running, total, _) -> if (total == 0) Palette.idle else if (running < total) Palette.bad else null },
                            onClick = { onOpen(stack.id) },
                        )
                    }
                }
            }
        }
    }
    if (creating) CreateStackDialog(api) { created -> creating = false; if (created != null) onOpen(created) }
}

@Composable
fun StackDetailScreen(api: VigaraApi, user: User, id: String, onBack: () -> Unit, onOpenContainer: (String, String) -> Unit) {
    val base = "/api/stacks/${Uri.encode(id)}"
    val detail = rememberLoad(id, pollMs = 20_000) { api.get<StackDetail>(base) }
    val summary = rememberLoad(id, pollMs = 15_000) { api.get<StackList>("/api/stacks").stacks.firstOrNull { it.id == id } to api.get<Fleet>("/api/containers").hosts }
    var tab by remember { mutableStateOf(0) }
    val snackbar = remember { SnackbarHostState() }
    val edit = remember(id) { StackEditState(api, id, base) }
    LaunchedEffect(id) { edit.run { edit.load() } }
    AppScaffold(summary.data?.first?.name?.ifBlank { id } ?: id, onBack = onBack, snackbar = snackbar) { padding ->
        Column(Modifier.padding(padding).fillMaxSize()) {
            PrimaryScrollableTabRow(tab, edgePadding = 8.dp) { listOf("Overview", "Files", "Env", "History", "Deploys").forEachIndexed { i, t -> Tab(tab == i, { tab = i }, text = { Text(t) }) } }
            when (tab) {
                0 -> LoadedContent(summary) { (stack, hosts) ->
                    if (stack == null) EmptyState("This stack no longer exists.")
                    else StackOverview(api, user, stack, hosts, detail.data, base, snackbar, onChanged = { summary.refresh(); detail.refresh() }, onOpenContainer = onOpenContainer)
                }
                1 -> StackFilesTab(edit, snackbar, user.canOperate)
                2 -> StackEnvTab(api, edit, user.canOperate, snackbar)
                3 -> StackHistory(api, base)
                else -> StackDeployments(api, user, base, snackbar)
            }
        }
    }
}

@Composable
private fun StackOverview(
    api: VigaraApi, user: User, stack: StackSummary, hosts: List<HostEntry>, detail: StackDetail?, base: String,
    snackbar: SnackbarHostState, onChanged: () -> Unit, onOpenContainer: (String, String) -> Unit,
) {
    val scope = rememberCoroutineScope()
    val host = hosts.firstOrNull { it.agentId == stack.agentId }
    val (label, color) = stackStatus(stack)
    var busy by remember { mutableStateOf(false) }
    var plan by remember { mutableStateOf<StackPlan?>(null) }
    var pull by remember { mutableStateOf(false) }
    var recreate by remember { mutableStateOf(false) }
    var confirmStop by remember { mutableStateOf(false) }
    val containers = host?.containers.orEmpty().filter { it.project == stack.projectName }
    val canDeploy = host?.canDeploy(user) == true && stack.commit.isNotBlank() && stack.assigned
    val canRun = host?.canManage(user) == true && containers.isNotEmpty() && host.state == "ok" && !host.truncated

    fun toAll(action: String) {
        scope.launch {
            busy = true
            val failures = mutableListOf<String>()
            containers.forEach { c ->
                try { runContainerAction(api, host!!.agentId, c.name.ifBlank { c.id }, action) } catch (e: ApiException) { failures += "${c.name}: ${e.message}" }
            }
            snackbar.showSnackbar(if (failures.isEmpty()) "Stack ${if (action == "start") "started" else "stopped"}." else failures.first())
            busy = false; onChanged()
        }
    }

    LazyColumn(contentPadding = PaddingValues(16.dp)) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically) { StatusChip(label, color) }
            Row(Modifier.padding(top = 10.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button({ scope.launch { busy = true; try { plan = api.post("$base/plan", buildJsonObject { put("commit", detail?.snapshot?.commit.orEmpty()) }); recreate = false; pull = false } catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "Could not prepare the deployment.") }; busy = false } }, enabled = canDeploy && !busy) {
                    if (busy) Spinner() else Text("Deploy…")
                }
                FilledTonalButton({ toAll("start") }, enabled = canRun && !busy) { Text("Start all") }
                OutlinedButton({ confirmStop = true }, enabled = canRun && !busy) { Text("Stop all") }
            }
            if (!canDeploy) InfoBanner(
                when {
                    !user.canOperate -> "Your account can view stacks but not deploy them."
                    !stack.assigned -> "This stack has no target device yet."
                    stack.commit.isBlank() -> "Save the stack in the web UI before deploying."
                    else -> "Deploying needs Full control (deploy tier) on the target host."
                },
                Modifier.padding(top = 8.dp),
            )
            SectionTitle("Details")
            KeyValue("Project", stack.projectName)
            KeyValue("Device", stack.deviceName.ifBlank { stack.agentId })
            KeyValue("Revision", stack.commit.take(8).ifEmpty { "Not saved" }, mono = true)
            KeyValue("Deployed", stack.deploymentCommit.take(8), mono = true)
            KeyValue("Last deploy", formatTime(stack.deploymentAt, ""))
            KeyValue("Services", stack.serviceCount.toString())
            KeyValue("Branch", detail?.branch.orEmpty())
            if (detail?.draft?.files != null) InfoBanner("This stack has an unsaved draft in the web UI.", Modifier.padding(top = 8.dp))
            SectionTitle("Containers")
            if (host == null || host.state != "ok") Text("The device is unavailable, so runtime state is unknown.", color = MaterialTheme.colorScheme.onSurfaceVariant)
            else if (containers.isEmpty()) Text("No containers are running for this project.", color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        items(containers, key = { it.id }) { c ->
            ItemCard(Modifier.padding(vertical = 4.dp), onClick = { onOpenContainer(host!!.agentId, c.name.ifBlank { c.id }) }) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(c.service.ifBlank { c.name }, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                    StatusChip(c.state, Palette.container(c.state))
                }
                Text(c.image, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }

    if (confirmStop) ConfirmDialog("Stop stack?", "All ${containers.size} containers in ${stack.projectName} will be stopped.", "Stop all",
        onConfirm = { confirmStop = false; toAll("stop") }, onDismiss = { confirmStop = false })

    plan?.let { p ->
        androidx.compose.material3.AlertDialog(
            onDismissRequest = { if (!busy) plan = null },
            title = { Text("Deploy ${stack.name.ifBlank { stack.id }}") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text("Apply revision ${detail?.snapshot?.commit?.take(8)} to project ${p.projectName}?")
                    Text("Services: ${p.services.joinToString(", ")}", style = MaterialTheme.typography.bodySmall)
                    p.images.forEach { (svc, img) -> Text("$svc: $img", style = MaterialTheme.typography.labelSmall) }
                    Text("Changed services may restart. Existing data volumes are retained.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(pull, { pull = it }); Text("Pull newer images") }
                    Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(recreate, { recreate = it }); Text("Recreate all containers") }
                }
            },
            confirmButton = {
                Button(enabled = !busy, onClick = {
                    scope.launch {
                        busy = true
                        try {
                            api.postUnit("$base/deploy", buildJsonObject {
                                put("commit", detail?.snapshot?.commit.orEmpty()); put("token", p.token)
                                put("images", kotlinx.serialization.json.JsonObject(p.images.mapValues { kotlinx.serialization.json.JsonPrimitive(it.value) }))
                                put("pull", pull); put("recreate", recreate)
                            })
                            plan = null
                            snackbar.showSnackbar("Deployment queued. Follow it on the Deploys tab.")
                            onChanged()
                        } catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "The deployment could not be started.") }
                        busy = false
                    }
                }) { Text("Deploy") }
            },
            dismissButton = { TextButton({ plan = null }, enabled = !busy) { Text("Cancel") } },
        )
    }
}

@Composable
private fun StackHistory(api: VigaraApi, base: String) {
    val history = rememberLoad(base) { api.get<List<StackRevision>>("$base/history") }
    LoadedContent(history) { list ->
        if (list.isEmpty()) { EmptyState("No saved revisions yet."); return@LoadedContent }
        LazyColumn(contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            items(list, key = { it.commit }) { r ->
                ItemCard {
                    Text(r.message, style = MaterialTheme.typography.titleSmall)
                    Text("${r.commit.take(8)} · ${formatTime(r.at)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }
    }
}

@Composable
private fun StackDeployments(api: VigaraApi, user: User, base: String, snackbar: SnackbarHostState) {
    val jobs = rememberLoad(base, pollMs = 5_000) { api.get<List<StackDeployment>>("$base/deployments") }
    val scope = rememberCoroutineScope()
    LoadedContent(jobs) { list ->
        if (list.isEmpty()) { EmptyState("No deployments yet."); return@LoadedContent }
        LazyColumn(contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            items(list, key = { it.id }) { job ->
                ItemCard {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("Revision ${job.commit.take(8)}", style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                        StatusChip(job.status, Palette.deployment(job.status))
                    }
                    Text("${job.stage} · ${job.message.ifBlank { "Waiting for the device." }}", style = MaterialTheme.typography.bodySmall)
                    Text(formatTime(job.createdAt), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    if (job.status == "unknown" && user.canOperate) {
                        TextButton({
                            scope.launch {
                                try { snackbar.showSnackbar(api.post<MessageResult>("$base/deployments/${job.id}/reconcile").message.ifBlank { "Checked." }) }
                                catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "Could not check the device.") }
                                jobs.refresh()
                            }
                        }) { Text("Check device result") }
                    }
                }
            }
        }
    }
}
