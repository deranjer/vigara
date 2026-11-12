package org.vigara.mobile.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.PrimaryScrollableTabRow
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import org.vigara.mobile.data.Availability
import org.vigara.mobile.data.BackupJob
import org.vigara.mobile.data.BackupPolicy
import org.vigara.mobile.data.Device
import org.vigara.mobile.data.DevicePage
import org.vigara.mobile.data.History
import org.vigara.mobile.data.Incident
import org.vigara.mobile.data.User
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.ItemCard
import org.vigara.mobile.ui.common.KeyValue
import org.vigara.mobile.ui.common.LineChart
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.formatBytes
import org.vigara.mobile.ui.common.formatDuration
import org.vigara.mobile.ui.common.formatMetricValue
import org.vigara.mobile.ui.common.formatTime
import org.vigara.mobile.ui.common.relativeTime
import org.vigara.mobile.ui.common.rememberLoad
import org.vigara.mobile.ui.common.seriesColors
import java.util.Locale

private fun Device.metric(name: String) = latest?.metrics?.firstOrNull { it.name == name }?.value

private val metricLabels = mapOf(
    "cpu_used_percent" to "CPU use", "memory_used_percent" to "Memory use", "memory_available_bytes" to "Available memory",
    "memory_total_bytes" to "Total memory", "filesystem_free_bytes" to "Free disk space", "filesystem_total_bytes" to "Total disk space",
    "network_receive_bytes_per_second" to "Network receive", "network_send_bytes_per_second" to "Network send",
    "disk_read_bytes_per_second" to "Disk read", "disk_write_bytes_per_second" to "Disk write", "uptime_seconds" to "Uptime",
    "docker_cpu_capacity_used_percent" to "Container CPU", "docker_memory_used_percent" to "Container memory",
)

fun prettyMetric(name: String) = metricLabels[name] ?: name.replace('_', ' ').replaceFirstChar { it.titlecase(Locale.getDefault()) }

@Composable
fun DevicesScreen(api: VigaraApi, user: User, onOpen: (String) -> Unit, onAdd: () -> Unit, onBack: (() -> Unit)? = null) {
    val devices = rememberLoad(pollMs = 30_000) { api.get<DevicePage>("/api/inventory/devices?limit=100&offset=0") }
    AppScaffold("Devices", onBack = onBack, actions = { if (user.isAdmin) androidx.compose.material3.TextButton(onAdd) { Text("Add") } }) {
        Column(Modifier.padding(it)) {
            LoadedContent(devices) { page ->
                if (page.devices.isEmpty()) EmptyState("No devices yet. Enroll one from the Vigara web UI (Devices → Add device).")
                else LazyColumn(contentPadding = androidx.compose.foundation.layout.PaddingValues(bottom = 16.dp)) {
                    items(page.devices.sortedBy { d -> d.name.lowercase() }, key = { d -> d.id }) { device -> DeviceRow(device) { onOpen(device.id) } }
                }
            }
        }
    }
}

@Composable
fun DeviceDetailScreen(api: VigaraApi, user: User, id: String, onBack: () -> Unit) {
    // The list endpoint carries the latest snapshot; the detail page reuses it.
    val device = rememberLoad(id, pollMs = 30_000) {
        api.get<DevicePage>("/api/inventory/devices?limit=100&offset=0").devices.firstOrNull { it.id == id }
            ?: throw org.vigara.mobile.data.ApiException(404, "This device no longer exists.")
    }
    var tab by remember { mutableStateOf(0) }
    val shellOk = user.canOperate && device.data?.let { it.agentId != null && it.platform in setOf("linux", "windows") && "host.exec" in it.capabilities } == true
    val titles = listOf("Overview", "Trends", "Alerts", "Backups", "Schedule") + if (shellOk) listOf("Shell") else emptyList()
    AppScaffold(device.data?.name ?: "Device", onBack = onBack) { padding ->
        Column(Modifier.padding(padding).fillMaxSize()) {
            PrimaryScrollableTabRow(tab, edgePadding = 8.dp) {
                titles.forEachIndexed { i, t -> Tab(tab == i, { tab = i }, text = { Text(t) }) }
            }
            LoadedContent(device) { d ->
                when (tab) {
                    0 -> DeviceOverview(d)
                    1 -> DeviceTrends(api, d)
                    2 -> DeviceAlerts(api, d)
                    3 -> DeviceBackups(api, user, d)
                    4 -> DeviceSchedule(api, d)
                    else -> TerminalPanel(
                        api, "/api/containers/${android.net.Uri.encode(d.agentId.orEmpty())}/shell", d.name, null,
                        "This opens a shell on the device itself, running as the agent service account (often root or LocalSystem).",
                        if (d.platform == "windows") listOf("auto" to "PowerShell (pwsh if available)", "powershell" to "PowerShell") else listOf("auto" to "Auto", "sh" to "sh", "bash" to "bash"),
                        allowUser = false,
                    )
                }
            }
        }
    }
}

@Composable
private fun DeviceOverview(device: Device) {
    LazyColumn(contentPadding = androidx.compose.foundation.layout.PaddingValues(16.dp)) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusChip(device.status.replaceFirstChar { it.titlecase() }, Palette.device(device.status))
                Text("  Seen ${relativeTime(device.lastSeenAt, "never")}", style = MaterialTheme.typography.bodySmall)
            }
            SectionTitle("Host")
            val host = device.latest?.host
            KeyValue("Platform", "${device.platform} ${device.architecture}".trim())
            KeyValue("OS", "${host?.os.orEmpty()} ${host?.osVersion.orEmpty()}".trim())
            KeyValue("CPU", host?.cpuModel.orEmpty())
            KeyValue("Logical CPUs", host?.logicalCpus?.takeIf { it > 0 }?.toString().orEmpty())
            KeyValue("Memory", host?.totalMemoryBytes?.takeIf { it > 0 }?.let { formatBytes(it) }.orEmpty())
            KeyValue("Uptime", host?.uptimeSeconds?.takeIf { it > 0 }?.let { formatDuration(it) }.orEmpty())
            KeyValue("Agent", device.agentVersion)
            KeyValue("Observed", formatTime(device.latest?.observedAt ?: device.telemetryObservedAt))
            val errors = device.collectionErrors.ifEmpty { device.latest?.errors.orEmpty() }
            if (errors.isNotEmpty()) {
                SectionTitle("Collection problems")
                errors.forEach { ErrorBanner("${it.capability}: ${it.message}", Modifier.padding(bottom = 6.dp)) }
            }
            SectionTitle("Current metrics")
        }
        val metrics = device.latest?.metrics.orEmpty().sortedBy { it.name }
        if (metrics.isEmpty()) item { Text("No telemetry has been received yet.", color = MaterialTheme.colorScheme.onSurfaceVariant) }
        items(metrics) { m ->
            val label = prettyMetric(m.name) + if (m.labels.isEmpty()) "" else " (" + m.labels.values.joinToString(", ") + ")"
            KeyValue(label, formatMetricValue(m.value, m.unit))
        }
    }
}

private val ranges = listOf("1h" to "1 hour", "6h" to "6 hours", "24h" to "24 hours", "7d" to "7 days", "30d" to "30 days", "90d" to "90 days")

@Composable
private fun DeviceTrends(api: VigaraApi, device: Device) {
    var range by remember { mutableStateOf("24h") }
    var selected by remember { mutableStateOf(listOf("cpu_used_percent")) }
    val history = rememberLoad(listOf(device.id, range, selected)) {
        val query = "range=$range" + selected.joinToString("") { "&metrics=$it" }
        api.get<History>("/api/inventory/devices/${device.id}/history?$query")
    }
    Column(Modifier.fillMaxSize()) {
        Row(Modifier.padding(horizontal = 12.dp).fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            ranges.take(4).forEach { (v, _) -> FilterChip(range == v, { range = v }, label = { Text(v) }) }
        }
        Row(Modifier.padding(horizontal = 12.dp).fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            ranges.drop(4).forEach { (v, _) -> FilterChip(range == v, { range = v }, label = { Text(v) }) }
        }
        LoadedContent(history) { h ->
            LazyColumn(contentPadding = androidx.compose.foundation.layout.PaddingValues(16.dp)) {
                item {
                    if (h.series.isEmpty()) Text("No data for the selected metric and range.", color = MaterialTheme.colorScheme.onSurfaceVariant)
                    h.series.groupBy { it.name }.forEach { (name, group) ->
                        Text(prettyMetric(name), style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(top = 8.dp))
                        LineChart(group, group.first().unit)
                        group.forEachIndexed { i, s ->
                            if (s.labels.isNotEmpty()) Text("■ " + s.labels.values.joinToString(", "), color = seriesColors[i % seriesColors.size], style = MaterialTheme.typography.labelSmall)
                        }
                    }
                    SectionTitle("Metric")
                }
                item {
                    // Wrapping rows of available metrics; tapping one swaps the chart.
                    val names = (h.metrics + selected).distinct().sorted()
                    names.chunked(2).forEach { pair ->
                        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            pair.forEach { n -> FilterChip(n in selected, { selected = listOf(n) }, label = { Text(prettyMetric(n), maxLines = 1) }) }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun DeviceAlerts(api: VigaraApi, device: Device) {
    val incidents = rememberLoad(device.id, pollMs = 30_000) { api.get<List<Incident>>("/api/alerts/incidents").filter { it.deviceId == device.id } }
    LoadedContent(incidents) { list ->
        if (list.isEmpty()) { EmptyState("No alerts for this device."); return@LoadedContent }
        LazyColumn(contentPadding = androidx.compose.foundation.layout.PaddingValues(bottom = 16.dp)) {
            items(list.sortedWith(compareBy({ it.status != "firing" }, { it.startedAt })), key = { it.id }) { incident ->
                org.vigara.mobile.ui.common.TwoLineRow(
                    incident.summary.ifBlank { incident.dimension },
                    listOf(incident.ruleName, if (incident.status == "firing") "started ${formatTime(incident.startedAt)}" else "resolved ${formatTime(incident.resolvedAt)}").filter { it.isNotBlank() }.joinToString(" · "),
                    dot = severityColor(incident),
                    trailing = relativeTime(incident.startedAt),
                )
            }
        }
    }
}

@Composable
internal fun DeviceBackupRuns(api: VigaraApi, device: Device) {
    val policies = rememberLoad(device.id) { api.get<List<BackupPolicy>>("/api/backups/policies").filter { it.deviceId == device.id } }
    LoadedContent(policies) { list ->
        if (list.isEmpty()) { EmptyState("No backup policies for this device. Configure backups in the web UI."); return@LoadedContent }
        LazyColumn(contentPadding = androidx.compose.foundation.layout.PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            items(list, key = { it.id }) { policy -> PolicyCard(api, policy) }
        }
    }
}

@Composable
private fun PolicyCard(api: VigaraApi, policy: BackupPolicy) {
    val jobs = rememberLoad(policy.id, pollMs = 20_000) { api.get<List<BackupJob>>("/api/backups/policies/${policy.id}/jobs") }
    ItemCard {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(policy.name, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
            StatusChip(if (policy.enabled) "Enabled" else "Disabled", if (policy.enabled) Palette.good else Palette.idle)
        }
        KeyValue("Destination", policy.destination)
        KeyValue("Paths", policy.includedPaths.joinToString(", "))
        SectionTitle("Recent runs")
        val list = jobs.data
        when {
            list == null && jobs.error != null -> Text(jobs.error!!, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
            list == null -> Spinner()
            list.isEmpty() -> Text("No runs yet.", style = MaterialTheme.typography.bodySmall)
            else -> list.take(6).forEach { job ->
                val outcome = job.outcome.ifBlank { job.executionStatus.ifBlank { job.status } }
                Row(Modifier.fillMaxWidth().padding(vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(formatTime(job.scheduledFor), style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f))
                    job.progress?.percentDone?.let { Text("${"%.0f".format(it)}%  ", style = MaterialTheme.typography.bodySmall) }
                    StatusChip(outcome, when (outcome) { "succeeded", "success", "completed" -> Palette.good; "failed", "error" -> Palette.bad; else -> Palette.warn })
                }
                if (job.errorMessage.isNotBlank()) Text(job.errorMessage, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
            }
        }
    }
}

@Composable
private fun DeviceSchedule(api: VigaraApi, device: Device) {
    val availability = rememberLoad(device.id) { api.get<Availability>("/api/devices/${device.id}/availability") }
    LoadedContent(availability) { a ->
        LazyColumn(contentPadding = androidx.compose.foundation.layout.PaddingValues(16.dp)) {
            item {
                InfoBanner("Availability rules tell Vigara when this device is expected to be awake, so sleeping machines do not raise offline alerts. Edit them in the web UI.")
                SectionTitle("Now")
                KeyValue("Expected awake", if (a.expectedAwake) "Yes" else "No")
                KeyValue("Maintenance", if (a.maintenanceActive) "Active" else "None")
                SectionTitle("Awake schedule")
                val awake = a.awake
                if (awake == null) Text("Always expected to be online.", color = MaterialTheme.colorScheme.onSurfaceVariant)
                else {
                    KeyValue("Days", awake.weekdays.joinToString(", "))
                    KeyValue("Hours", "${awake.start} – ${awake.end}")
                    KeyValue("Time zone", awake.timezone)
                }
                if (a.maintenance.isNotEmpty()) {
                    SectionTitle("Maintenance windows")
                    a.maintenance.forEach { KeyValue(formatTime(it.startsAt), "until ${formatTime(it.endsAt)}") }
                }
            }
        }
    }
}
