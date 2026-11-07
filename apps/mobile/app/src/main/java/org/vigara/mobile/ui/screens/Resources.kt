package org.vigara.mobile.ui.screens

import android.net.Uri
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
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
import org.vigara.mobile.data.ImageList
import org.vigara.mobile.data.NetworkList
import org.vigara.mobile.data.PruneResult
import org.vigara.mobile.data.User
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.data.VolumeList
import org.vigara.mobile.data.str
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ChipRow
import org.vigara.mobile.ui.common.ConfirmDialog
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.ItemCard
import org.vigara.mobile.ui.common.KeyValue
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.formatBytes
import org.vigara.mobile.ui.common.formatEpoch
import org.vigara.mobile.ui.common.rememberLoad
import org.vigara.mobile.ui.common.shortId

private data class ResRow(val ref: String, val title: String, val subtitle: String, val meta: List<Pair<String, String>>, val usedBy: List<String>, val chip: String? = null, val protectedRow: Boolean = false)

@Composable
fun ResourcesScreen(api: VigaraApi, user: User, kind: String, onBack: () -> Unit) {
    val title = when (kind) { "image" -> "Images"; "network" -> "Networks"; else -> "Volumes" }
    val plural = when (kind) { "image" -> "images"; "network" -> "networks"; else -> "volumes" }
    val fleet = rememberLoad { api.get<Fleet>("/api/containers").hosts.filter { it.state == "ok" } }
    var hostId by remember { mutableStateOf<String?>(null) }
    var search by remember { mutableStateOf("") }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    var pruning by remember { mutableStateOf(false) }
    var pulling by remember { mutableStateOf(false) }
    var removing by remember { mutableStateOf<ResRow?>(null) }
    var force by remember { mutableStateOf(false) }

    val hosts = fleet.data.orEmpty()
    val host: HostEntry? = hosts.firstOrNull { it.agentId == hostId } ?: hosts.firstOrNull()
    val base = host?.let { "/api/docker/${Uri.encode(it.agentId)}/$plural" }
    val rows = rememberLoad(base, pollMs = 20_000) {
        if (base == null) emptyList() else when (kind) {
            "image" -> api.get<ImageList>(base).images.orEmpty().sortedByDescending { it.created }.map {
                ResRow(it.id, it.tags.firstOrNull() ?: "<untagged> ${shortId(it.id)}", shortId(it.id), listOf("Size" to formatBytes(it.size), "Created" to formatEpoch(it.created)), it.usedBy.map { u -> u.name }, if (it.dangling) "dangling" else null)
            }
            "network" -> api.get<NetworkList>(base).networks.orEmpty().sortedBy { it.name }.map {
                ResRow(it.id, it.name, "${it.driver} · ${it.scope}", listOf("Subnets" to it.subnets.joinToString(", ")), it.usedBy.map { u -> u.name }, if (it.builtin) "built-in" else null, it.builtin)
            }
            else -> api.get<VolumeList>(base).volumes.orEmpty().sortedBy { it.name }.map {
                ResRow(it.name, it.name, it.driver, listOf("Size" to if (it.size >= 0) formatBytes(it.size) else "", "Mountpoint" to it.mountpoint), it.usedBy.map { u -> u.name }, if (it.anonymous) "anonymous" else null)
            }
        }
    }
    val mayManage = host?.canManage(user) == true

    AppScaffold(
        title, onBack = onBack, snackbar = snackbar,
        actions = {
            if (mayManage && kind == "image") TextButton({ pulling = true }) { Text("Pull") }
            if (mayManage) TextButton({ pruning = true }) { Text("Prune") }
        },
    ) { padding ->
        Column(Modifier.padding(padding)) {
            if (hosts.size > 1) ChipRow(hosts.map { it.agentId to it.name }, host?.agentId, { hostId = it ?: hostId }, "")
            OutlinedTextField(search, { search = it }, singleLine = true, placeholder = { Text("Search") }, modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp))
            if (host != null && !mayManage) InfoBanner(if (!user.canOperate) "Your account can view but not change Docker resources." else "Management is not enabled for ${host.name}.", Modifier.padding(horizontal = 12.dp))
            if (fleet.data != null && hosts.isEmpty()) EmptyState("No connected hosts with Docker enabled.")
            else LoadedContent(rows) { list ->
                val needle = search.trim().lowercase()
                val shown = list.filter { needle.isEmpty() || (it.title + it.subtitle + it.ref).lowercase().contains(needle) }
                if (shown.isEmpty()) EmptyState("Nothing to show.")
                else LazyColumn(contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    items(shown, key = { it.ref }) { row ->
                        ItemCard {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text(row.title, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                                row.chip?.let { StatusChip(it, Palette.idle) }
                            }
                            Text(row.subtitle, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            row.meta.forEach { (k, v) -> KeyValue(k, v) }
                            KeyValue("Used by", row.usedBy.joinToString(", ").ifEmpty { "Nothing" })
                            if (mayManage && !row.protectedRow) TextButton({ removing = row; force = false }) { Text("Remove", color = MaterialTheme.colorScheme.error) }
                        }
                    }
                }
            }
        }
    }

    removing?.let { row ->
        ConfirmDialog(
            "Remove ${row.title}?",
            if (row.usedBy.isNotEmpty()) "It is in use by ${row.usedBy.joinToString(", ")}; Docker will normally refuse." else "This cannot be undone.",
            "Remove", destructive = true,
            onConfirm = {
                removing = null
                scope.launch {
                    try {
                        api.postUnit("$base/remove", buildJsonObject { put("ref", row.ref); put("force", kind == "image" && force) })
                        snackbar.showSnackbar("Removed."); rows.refresh()
                    } catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "Could not remove it.") }
                }
            },
            onDismiss = { removing = null },
            extra = { if (kind == "image") Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(force, { force = it }); Text("Force") } },
        )
    }
    if (pruning && base != null) PruneDialog(api, base, kind, onDone = { pruning = false; rows.refresh() }, onDismiss = { pruning = false })
    if (pulling && base != null) PullDialog(api, base, onDone = { pulling = false; rows.refresh() })
}

@Composable
private fun PruneDialog(api: VigaraApi, base: String, kind: String, onDone: () -> Unit, onDismiss: () -> Unit) {
    var all by remember { mutableStateOf(false) }
    var preview by remember { mutableStateOf<PruneResult?>(null) }
    var result by remember { mutableStateOf<PruneResult?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    LaunchedEffect(all) {
        busy = true; error = null; preview = null
        try { preview = api.post("$base/prune", buildJsonObject { put("all", all); put("dryRun", true) }) } catch (e: ApiException) { error = e.message }
        busy = false
    }
    AlertDialog(
        onDismissRequest = { if (!busy) onDismiss() },
        title = { Text("Prune unused ${kind}s") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (kind == "image") Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(all, { all = it }); Text("Include all unused images, not just dangling") }
                error?.let { ErrorBanner(it) }
                when {
                    result != null -> Text("Removed ${result!!.removed.size} item(s), reclaimed ${formatBytes(result!!.spaceReclaimed)}.")
                    busy && preview == null -> Spinner()
                    preview != null -> {
                        val c = preview!!.candidates
                        Text(if (c.isEmpty()) "Nothing to remove." else "${c.size} item(s) would be removed:")
                        c.take(12).forEach { Text("• ${it.name.ifBlank { shortId(it.id) }}", style = MaterialTheme.typography.bodySmall) }
                        if (c.size > 12) Text("…and ${c.size - 12} more", style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
        },
        confirmButton = {
            if (result != null) TextButton(onDone) { Text("Done") }
            else Button(enabled = !busy && preview?.candidates?.isNotEmpty() == true, onClick = {
                scope.launch {
                    busy = true
                    try { result = api.post("$base/prune", buildJsonObject { put("all", all); put("dryRun", false) }) } catch (e: ApiException) { error = e.message }
                    busy = false
                }
            }) { Text("Prune") }
        },
        dismissButton = { if (result == null) TextButton(onDismiss, enabled = !busy) { Text("Cancel") } },
    )
}

@Composable
private fun PullDialog(api: VigaraApi, base: String, onDone: () -> Unit) {
    var ref by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var finished by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var progress by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    AlertDialog(
        onDismissRequest = { if (!busy) onDone() },
        title = { Text("Pull image") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(ref, { ref = it }, label = { Text("Image reference") }, placeholder = { Text("nginx:stable") }, singleLine = true, enabled = !busy && !finished)
                if (progress.isNotEmpty()) Text(progress, style = MaterialTheme.typography.bodySmall)
                error?.let { ErrorBanner(it) }
                if (busy) Spinner()
            }
        },
        confirmButton = {
            if (finished) TextButton(onDone) { Text("Done") }
            else Button(enabled = !busy && ref.isNotBlank(), onClick = {
                scope.launch {
                    busy = true; error = null
                    try {
                        api.postLines("$base/pull", buildJsonObject { put("ref", ref.trim()) }) { line ->
                            if (line.str("step") == "error") error = line.str("message") ?: "The pull failed."
                            else progress = listOfNotNull(line.str("step"), line.str("message")).joinToString(": ")
                        }
                        finished = error == null
                    } catch (e: ApiException) { error = e.message }
                    busy = false
                }
            }) { Text("Pull") }
        },
        dismissButton = { if (!finished) TextButton(onDone, enabled = !busy) { Text("Cancel") } },
    )
}
