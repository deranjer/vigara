package org.vigara.mobile.ui.screens

import android.net.Uri
import androidx.compose.foundation.clickable
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
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
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
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.vigara.mobile.data.AlertRule
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.AppJson
import org.vigara.mobile.data.BackupSnapshot
import org.vigara.mobile.data.Device
import org.vigara.mobile.data.DevicePage
import org.vigara.mobile.data.ManagedConfig
import org.vigara.mobile.data.ManagedView
import org.vigara.mobile.data.SnapshotEntry
import org.vigara.mobile.data.User
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.ItemCard
import org.vigara.mobile.ui.common.KeyValue
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.formatBytes
import org.vigara.mobile.ui.common.formatTime
import org.vigara.mobile.ui.common.rememberDownloader
import org.vigara.mobile.ui.common.rememberLoad

private val weekdayNames = listOf("Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday")
private val setupErrors = mapOf(
    "source_unavailable" to "A selected folder cannot be read by the agent.",
    "repository_unavailable" to "The repository cannot be opened or initialized. Check storage access and the repository password.",
    "restic_unavailable" to "The bundled restic executable is missing. Install a fresh backup-enabled agent bundle.",
    "invalid_configuration" to "The selected paths are invalid or include the backup repository.",
    "state_unavailable" to "The agent could not save its private configuration.",
)

@Composable
fun DeviceBackups(api: VigaraApi, user: User, device: Device) {
    val base = "/api/backups/devices/${Uri.encode(device.id)}"
    val view = rememberLoad(device.id, pollMs = 15_000) { api.get<ManagedView>("$base/managed") }
    var editing by remember { mutableStateOf<ManagedConfig?>(null) }
    var browsing by remember { mutableStateOf(false) }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    val download = rememberDownloader(api) { scope.launch { snackbar.showSnackbar(it) } }

    LoadedContent(view) { v ->
        val ready = v.configured && v.revision == v.appliedRevision && v.errorCode.isEmpty()
        LazyColumn(contentPadding = PaddingValues(16.dp)) {
            item {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Backup setup", style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                    if (v.configured) StatusChip(if (ready) "Ready" else if (v.errorCode.isNotEmpty()) "Needs attention" else "Changes pending", if (ready) Palette.good else if (v.errorCode.isNotEmpty()) Palette.bad else Palette.info)
                }
                if (!v.available) InfoBanner("The server needs a backup master key before managed backups can be configured.", Modifier.padding(top = 8.dp))
                val c = v.config
                if (c != null) {
                    KeyValue("Folders", c.sourcePaths.joinToString("\n"), mono = true)
                    KeyValue("Repository", c.repository, mono = true)
                    KeyValue("Schedule", if (c.enabled) "${c.schedule.weekdays.joinToString(", ") { it.take(3) }} at ${c.schedule.time} (${c.schedule.timezone})" else "Paused")
                    KeyValue("Revision", "desired ${v.revision}, applied ${v.appliedRevision}")
                    if (v.errorCode.isNotEmpty()) ErrorBanner(setupErrors[v.errorCode] ?: "The agent could not apply the backup configuration.", Modifier.padding(top = 8.dp))
                    else if (!ready) Text("Waiting for the device to apply these settings.", style = MaterialTheme.typography.bodySmall)
                } else Text("No managed backup yet. Choose folders and a schedule; the backup-enabled agent configures restic automatically.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Row(Modifier.padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (user.canOperate) Button({
                        editing = c?.copy(expectedRevision = v.revision, password = null) ?: ManagedConfig(schedule = org.vigara.mobile.data.BackupSchedule(weekdayNames, "02:00", java.util.TimeZone.getDefault().id))
                    }, enabled = v.available) { Text(if (v.configured) "Edit" else "Set up") }
                    if (c?.serverRepository?.isNotBlank() == true) OutlinedButton({ browsing = !browsing }) { Text(if (browsing) "Close snapshots" else "Snapshots") }
                    if (v.configured) OutlinedButton({ download("GET", "$base/managed/recovery", "vigara-recovery-${device.name}.json", null, null) }) { Text("Recovery") }
                }
            }
            if (browsing) item { SnapshotBrowser(api, base, device.name, snackbar, download) }
            item { SectionTitle("Backup policies and runs") }
            item { DeviceBackupRuns(api, device) }
            item { androidx.compose.material3.SnackbarHost(snackbar) }
        }
    }

    editing?.let { cfg ->
        BackupEditDialog(api, base, cfg, existing = view.data?.initialized == true, onDone = { saved -> editing = null; if (saved) view.refresh() })
    }
}

@Composable
private fun BackupEditDialog(api: VigaraApi, base: String, initial: ManagedConfig, existing: Boolean, onDone: (Boolean) -> Unit) {
    var paths by remember { mutableStateOf(initial.sourcePaths.joinToString("\n")) }
    var repository by remember { mutableStateOf(initial.repository) }
    var serverRepository by remember { mutableStateOf(initial.serverRepository) }
    var initialize by remember { mutableStateOf(initial.initialize) }
    var password by remember { mutableStateOf("") }
    var days by remember { mutableStateOf(initial.schedule.weekdays.toSet()) }
    var time by remember { mutableStateOf(initial.schedule.time) }
    var zone by remember { mutableStateOf(initial.schedule.timezone) }
    var enabled by remember { mutableStateOf(initial.enabled) }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val pathList = paths.lines().map { it.trim() }.filter { it.isNotEmpty() }
    val valid = pathList.isNotEmpty() && repository.isNotBlank() && days.isNotEmpty() && Regex("^\\d{2}:\\d{2}$").matches(time) && (existing || initialize || password.isNotEmpty())

    AlertDialog(
        onDismissRequest = { if (!busy) onDone(false) },
        title = { Text("Backup settings") },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                error?.let { ErrorBanner(it) }
                OutlinedTextField(paths, { paths = it }, label = { Text("Folders to back up (one per line)") }, minLines = 2, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(repository, { repository = it }, label = { Text("Repository path on the device") }, singleLine = true, enabled = !existing, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(serverRepository, { serverRepository = it }, label = { Text("Same repository as the server sees it (for browsing)") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                if (!existing) {
                    Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(initialize, { initialize = it; password = "" }); Text("Create a new encrypted repository") }
                    if (!initialize) OutlinedTextField(password, { password = it }, label = { Text("Existing repository password") }, singleLine = true, visualTransformation = PasswordVisualTransformation(), modifier = Modifier.fillMaxWidth())
                }
                Text("Days", style = MaterialTheme.typography.labelMedium)
                weekdayNames.chunked(4).forEach { chunk ->
                    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        chunk.forEach { d -> FilterChip(d in days, { days = if (d in days) days - d else days + d }, label = { Text(d.take(3)) }) }
                    }
                }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedTextField(time, { time = it }, label = { Text("Time HH:MM") }, singleLine = true, modifier = Modifier.weight(1f))
                    OutlinedTextField(zone, { zone = it }, label = { Text("Time zone") }, singleLine = true, modifier = Modifier.weight(1.4f))
                }
                Row(verticalAlignment = Alignment.CenterVertically) { Switch(enabled, { enabled = it }); Text("  Scheduled backups enabled") }
                Text("Changes apply after the device connects. A running backup must finish first.", style = MaterialTheme.typography.bodySmall)
            }
        },
        confirmButton = {
            Button(enabled = valid && !busy, onClick = {
                scope.launch {
                    busy = true; error = null
                    try {
                        val body = buildJsonObject {
                            put("repository", repository.trim()); put("serverRepository", serverRepository.trim())
                            put("sourcePaths", kotlinx.serialization.json.JsonArray(pathList.map { JsonPrimitive(it) }))
                            if (!existing && !initialize) put("password", password)
                            put("initialize", if (existing) false else initialize)
                            put("schedule", buildJsonObject {
                                put("weekdays", kotlinx.serialization.json.JsonArray(weekdayNames.filter { it in days }.map { JsonPrimitive(it) }))
                                put("time", time); put("timezone", zone.trim())
                            })
                            put("enabled", enabled); put("expectedRevision", initial.expectedRevision)
                        }
                        api.put<ManagedView>("$base/managed", body)
                        onDone(true)
                    } catch (e: ApiException) { error = e.message }
                    busy = false
                }
            }) { if (busy) Spinner() else Text("Save") }
        },
        dismissButton = { TextButton({ onDone(false) }, enabled = !busy) { Text("Cancel") } },
    )
}

@Composable
private fun SnapshotBrowser(
    api: VigaraApi, base: String, deviceName: String, snackbar: SnackbarHostState,
    download: (String, String, String, kotlinx.serialization.json.JsonElement?, ((okhttp3.Response) -> Unit)?) -> Unit,
) {
    val snapshots = rememberLoad(base) { api.get<List<BackupSnapshot>>("$base/snapshots").sortedByDescending { it.time } }
    var snapshot by remember { mutableStateOf<String?>(null) }
    var directory by remember { mutableStateOf("/") }
    val current = snapshot ?: snapshots.data?.firstOrNull()?.id
    val entries = rememberLoad(listOf(current, directory)) {
        if (current == null) emptyList() else api.get<List<SnapshotEntry>>("$base/snapshots/$current/entries?path=${Uri.encode(directory)}")
            .sortedWith(compareBy({ it.type != "dir" }, { it.name.lowercase() }))
    }
    Column {
        SectionTitle("Snapshots")
        snapshots.error?.let { if (snapshots.data == null) ErrorBanner(it) }
        val list = snapshots.data
        if (list == null) { if (snapshots.error == null) Spinner() }
        else if (list.isEmpty()) Text("No snapshots yet.", color = MaterialTheme.colorScheme.onSurfaceVariant)
        else {
            list.take(8).forEach { s ->
                FilterChip(current == s.id, { snapshot = s.id; directory = "/" }, label = { Text("${formatTime(s.time)} · ${s.id.take(8)}", maxLines = 1) }, modifier = Modifier.padding(bottom = 2.dp))
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(directory, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f), fontFamily = androidx.compose.ui.text.font.FontFamily.Monospace)
                if (directory != "/") TextButton({ directory = directory.trimEnd('/').substringBeforeLast('/').ifEmpty { "/" } }) { Text("Up") }
                if (current != null) TextButton({ download("GET", "$base/snapshots/$current/download?path=${Uri.encode(directory)}", "$deviceName-${current.take(8)}${if (directory == "/") "" else "-" + directory.trimEnd('/').substringAfterLast('/')}.zip", null, null) }) { Text("Download folder") }
            }
            entries.error?.let { ErrorBanner(it) }
            entries.data?.forEach { e ->
                ListItem(
                    headlineContent = { Text(if (e.type == "dir") "${e.name}/" else e.name) },
                    supportingContent = { if (e.type == "file") Text(formatBytes(e.size)) },
                    trailingContent = { if (e.type == "file" || e.type == "dir") TextButton({ download("GET", "$base/snapshots/$current/download?path=${Uri.encode(e.path)}", if (e.type == "dir") "${e.name}.zip" else e.name, null, null) }) { Text("Save") } },
                    modifier = Modifier.clickable(enabled = e.type == "dir") { directory = e.path },
                )
                HorizontalDivider()
            }
            if (entries.loading) Spinner()
        }
    }
}

// ---- Alert rules ----

@Composable
fun AlertRulesScreen(api: VigaraApi, user: User, onBack: () -> Unit) {
    val rules = rememberLoad(pollMs = 30_000) { api.get<List<AlertRule>>("/api/alerts/rules") }
    val devices = rememberLoad { api.get<DevicePage>("/api/inventory/devices?limit=100&offset=0").devices }
    var creating by remember { mutableStateOf(false) }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    AppScaffold("Alert rules", onBack = onBack, snackbar = snackbar, actions = { if (user.canOperate) TextButton({ creating = true }) { Text("Add") } }) { padding ->
        Column(Modifier.padding(padding)) {
            LoadedContent(rules) { list ->
                if (list.isEmpty()) EmptyState("No alert rules yet.")
                else LazyColumn(contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    items(list, key = { it.id }) { r ->
                        val deviceName = devices.data?.firstOrNull { it.id == r.config.deviceId }?.name ?: if (r.config.deviceId.isEmpty()) "All devices" else r.config.deviceId.take(8)
                        ItemCard {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text(r.name, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                                StatusChip(if (r.enabled) "Enabled" else "Disabled", if (r.enabled) Palette.good else Palette.idle)
                            }
                            Text("${r.kind.replace('_', ' ')} · $deviceName", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            Text(describe(r), style = MaterialTheme.typography.bodySmall)
                            if (r.enabled && user.canOperate) TextButton({
                                scope.launch {
                                    try { api.postUnit("/api/alerts/rules/${r.id}/disable") } catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "Could not disable the rule.") }
                                    rules.refresh()
                                }
                            }) { Text("Disable") }
                        }
                    }
                }
            }
        }
    }
    if (creating) CreateRuleDialog(api, devices.data.orEmpty(), onDone = { creating = false; rules.refresh() })
}

private fun describe(r: AlertRule): String = when (r.kind) {
    "device_unavailable" -> "Alert when no data for ${r.config.staleAfterSeconds / 60} min"
    "threshold" -> "Alert when ${r.config.metric.replace('_', ' ')} ${mapOf("gt" to ">", "gte" to "≥", "lt" to "<", "lte" to "≤")[r.config.operator] ?: r.config.operator} ${r.config.threshold} for ${r.config.sustainSeconds / 60} min"
    "backup_failure" -> "Alert when backup ${r.config.policyKey} fails"
    "backup_overdue" -> "Alert when backup ${r.config.policyKey} is older than ${r.config.intervalSeconds / 3600} h"
    else -> ""
}

@Composable
private fun CreateRuleDialog(api: VigaraApi, devices: List<Device>, onDone: () -> Unit) {
    var name by remember { mutableStateOf("") }
    var kind by remember { mutableStateOf("device_unavailable") }
    var deviceId by remember { mutableStateOf("") }
    var staleMin by remember { mutableStateOf("10") }
    var metric by remember { mutableStateOf("cpu_used_percent") }
    var operator by remember { mutableStateOf("gt") }
    var threshold by remember { mutableStateOf("90") }
    var sustainMin by remember { mutableStateOf("5") }
    var policyKey by remember { mutableStateOf("") }
    var overdueHours by remember { mutableStateOf("26") }
    var graceMin by remember { mutableStateOf("0") }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val needsDevice = kind == "backup_failure" || kind == "backup_overdue"

    AlertDialog(
        onDismissRequest = { if (!busy) onDone() },
        title = { Text("New alert rule") },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                error?.let { ErrorBanner(it) }
                OutlinedTextField(name, { name = it }, label = { Text("Name") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                listOf("device_unavailable" to "Device unavailable", "threshold" to "Metric threshold", "backup_failure" to "Backup failure", "backup_overdue" to "Backup overdue").chunked(2).forEach { pair ->
                    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) { pair.forEach { (k, l) -> FilterChip(kind == k, { kind = k }, label = { Text(l) }) } }
                }
                Text(if (needsDevice) "Device (required)" else "Device (optional)", style = MaterialTheme.typography.labelMedium)
                if (!needsDevice) FilterChip(deviceId.isEmpty(), { deviceId = "" }, label = { Text("All devices") })
                devices.forEach { d -> FilterChip(deviceId == d.id, { deviceId = d.id }, label = { Text(d.name) }) }
                when (kind) {
                    "device_unavailable" -> OutlinedTextField(staleMin, { staleMin = it }, label = { Text("No data for (minutes)") }, singleLine = true)
                    "threshold" -> {
                        Text("Metric", style = MaterialTheme.typography.labelMedium)
                        listOf("cpu_used_percent", "memory_used_percent", "filesystem_free_bytes", "filesystem_total_bytes", "memory_available_bytes").forEach { m -> FilterChip(metric == m, { metric = m }, label = { Text(m.replace('_', ' ')) }) }
                        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) { listOf("gt" to ">", "gte" to "≥", "lt" to "<", "lte" to "≤").forEach { (v, l) -> FilterChip(operator == v, { operator = v }, label = { Text(l) }) } }
                        OutlinedTextField(threshold, { threshold = it }, label = { Text(if (metric.endsWith("percent")) "Threshold (%)" else "Threshold (bytes)") }, singleLine = true)
                        OutlinedTextField(sustainMin, { sustainMin = it }, label = { Text("Sustained for (minutes)") }, singleLine = true)
                    }
                    else -> {
                        OutlinedTextField(policyKey, { policyKey = it }, label = { Text("Policy key") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                        if (kind == "backup_overdue") OutlinedTextField(overdueHours, { overdueHours = it }, label = { Text("Overdue after (hours)") }, singleLine = true)
                    }
                }
                OutlinedTextField(graceMin, { graceMin = it }, label = { Text("Grace period (minutes)") }, singleLine = true)
            }
        },
        confirmButton = {
            Button(enabled = !busy && name.isNotBlank() && (!needsDevice || deviceId.isNotEmpty()), onClick = {
                scope.launch {
                    busy = true; error = null
                    try {
                        val config = buildJsonObject {
                            if (deviceId.isNotEmpty()) put("deviceId", deviceId)
                            put("graceSeconds", (graceMin.toIntOrNull() ?: 0) * 60)
                            when (kind) {
                                "device_unavailable" -> put("staleAfterSeconds", (staleMin.toIntOrNull() ?: 10) * 60)
                                "threshold" -> { put("metric", metric); put("operator", operator); put("threshold", threshold.toDoubleOrNull() ?: 0.0); put("sustainSeconds", (sustainMin.toIntOrNull() ?: 0) * 60) }
                                "backup_failure" -> put("policyKey", policyKey.trim())
                                else -> { put("policyKey", policyKey.trim()); put("intervalSeconds", (overdueHours.toIntOrNull() ?: 26) * 3600) }
                            }
                        }
                        api.postUnit("/api/alerts/rules", buildJsonObject { put("name", name.trim()); put("kind", kind); put("config", config) })
                        onDone()
                    } catch (e: ApiException) { error = e.message }
                    busy = false
                }
            }) { if (busy) Spinner() else Text("Create") }
        },
        dismissButton = { TextButton(onDone, enabled = !busy) { Text("Cancel") } },
    )
}
