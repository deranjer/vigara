package org.vigara.mobile.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import org.vigara.mobile.data.Device
import org.vigara.mobile.data.DevicePage
import org.vigara.mobile.data.Fleet
import org.vigara.mobile.data.HostEntry
import org.vigara.mobile.data.Incident
import org.vigara.mobile.data.UpdateOverview
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.ListHeader
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.StatusDot
import org.vigara.mobile.ui.common.TwoLineRow
import org.vigara.mobile.ui.common.rememberLoad
import org.vigara.mobile.ui.common.relativeTime

/** Each source is loaded on its own so one failing endpoint still leaves the rest of the page. */
private data class OverviewData(
    val devices: List<Device>?, val hosts: List<HostEntry>?, val incidents: List<Incident>?, val updates: UpdateOverview?, val failed: List<String>,
)

private fun Device.percent(name: String) = latest?.metrics?.firstOrNull { it.name == name }?.value

/** One device as a two-line row: status, platform and last seen, with CPU and memory on the right. */
@Composable
fun DeviceRow(device: Device, onClick: () -> Unit) {
    val cpu = device.percent("cpu_used_percent")
    val mem = device.percent("memory_used_percent")
    val seen = if (device.status == "healthy") null else "seen ${relativeTime(device.lastSeenAt, "never")}"
    TwoLineRow(
        title = device.name,
        subtitle = listOfNotNull(device.status.replaceFirstChar { it.titlecase() }, "${device.platform} ${device.architecture}".trim().ifBlank { null }, seen).joinToString(" · "),
        dot = Palette.device(device.status),
        trailing = cpu?.let { "CPU ${"%.0f".format(it)}%" },
        trailingDetail = mem?.let { "Mem ${"%.0f".format(it)}%" },
        trailingColor = cpu?.let { if (it >= 90) Palette.bad else if (it >= 75) Palette.warn else null },
        onClick = onClick,
    )
}

@Composable
fun OverviewScreen(api: VigaraApi, onNavigate: (String) -> Unit, onOpenDevice: (String) -> Unit) {
    val data = rememberLoad(pollMs = 30_000) {
        val failed = mutableListOf<String>()
        suspend fun <T> part(label: String, block: suspend () -> T): T? = runCatching { block() }.onFailure { failed += label }.getOrNull()
        OverviewData(
            devices = part("devices") { api.get<DevicePage>("/api/inventory/devices?limit=100&offset=0").devices },
            hosts = part("containers") { api.get<Fleet>("/api/containers").hosts },
            incidents = part("alerts") { api.get<List<Incident>>("/api/alerts/incidents") },
            updates = part("image updates") { api.get<UpdateOverview>("/api/image-updates") },
            failed = failed,
        )
    }
    AppScaffold("Overview") { padding ->
        Column(Modifier.padding(padding)) {
            LoadedContent(data) { d ->
                val open = d.incidents.orEmpty().filter { it.bucket() == "open" }
                val containers = d.hosts.orEmpty().flatMap { it.containers }
                val updates = d.updates?.items.orEmpty().filter { !it.ignored && it.result.status == "available" }
                val devices = d.devices.orEmpty()
                val troubled = devices.filter { it.status == "stale" || it.status == "failed" }
                LazyColumn(contentPadding = PaddingValues(bottom = 16.dp)) {
                    if (d.failed.isNotEmpty()) item { ErrorBanner("Could not load ${d.failed.joinToString(", ")}.", Modifier.padding(12.dp)) }
                    item {
                        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Stat("Devices online", d.devices?.let { "${it.count { x -> x.status == "healthy" }}/${it.size}" }, if (troubled.isNotEmpty()) Palette.warn else null, Modifier.weight(1f)) { onNavigate("devices") }
                                Stat("Containers running", d.hosts?.let { "${containers.count { it.state == "running" }}/${containers.size}" }, null, Modifier.weight(1f)) { onNavigate("containers") }
                            }
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Stat("Open alerts", d.incidents?.let { "${open.size}" }, when { open.any { it.severity == "critical" } -> Palette.bad; open.isNotEmpty() -> Palette.warn; else -> null }, Modifier.weight(1f)) { onNavigate("alerts") }
                                Stat("Image updates", d.updates?.let { "${updates.size}" }, if (updates.isNotEmpty()) Palette.info else null, Modifier.weight(1f)) { onNavigate("updates") }
                            }
                        }
                    }
                    if (open.isNotEmpty() || troubled.isNotEmpty()) {
                        item { ListHeader("Needs attention", open.size + troubled.size) }
                        items(open.sortedBy { if (it.severity == "critical") 0 else 1 }.take(5), key = { "a-${it.id}" }) { incident ->
                            TwoLineRow(incident.summary.ifBlank { incident.dimension }, incident.ruleName, dot = severityColor(incident), trailing = relativeTime(incident.startedAt)) { onNavigate("alerts") }
                        }
                        items(troubled, key = { "t-${it.id}" }) { device -> DeviceRow(device) { onOpenDevice(device.id) } }
                    }
                    if (devices.isNotEmpty()) {
                        item { ListHeader("Devices", devices.size) }
                        items(devices.sortedBy { it.name.lowercase() }, key = { "d-${it.id}" }) { device -> DeviceRow(device) { onOpenDevice(device.id) } }
                    }
                }
            }
        }
    }
}

@Composable
private fun Stat(label: String, value: String?, tone: Color?, modifier: Modifier, onClick: () -> Unit) {
    val shape = RoundedCornerShape(8.dp)
    Column(
        modifier.background(MaterialTheme.colorScheme.surface, shape).border(1.dp, MaterialTheme.colorScheme.outlineVariant, shape)
            .clickable(onClick = onClick).padding(horizontal = 12.dp, vertical = 10.dp),
    ) {
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            if (tone != null) StatusDot(tone)
            Text(label.uppercase(), fontSize = 10.sp, letterSpacing = 0.6.sp, fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1)
        }
        Text(value ?: "—", fontSize = 22.sp, fontWeight = FontWeight.SemiBold, modifier = Modifier.padding(top = 2.dp).fillMaxWidth())
    }
}
