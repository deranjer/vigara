package org.vigara.mobile.ui.screens

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.Incident
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.KeyValue
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.StatusText
import org.vigara.mobile.ui.common.TwoLineRow
import org.vigara.mobile.ui.common.formatTime
import org.vigara.mobile.ui.common.rememberLoad
import org.vigara.mobile.ui.common.relativeTime

private val buckets = listOf("open" to "Open", "acknowledged" to "Acknowledged", "muted" to "Muted", "resolved" to "Resolved")

fun severityColor(incident: Incident): Color = when {
    incident.status != "firing" -> Palette.good
    incident.severity == "critical" -> Palette.bad
    else -> Palette.warn
}

/** Critical first, then the longest-running, as in the web inbox. */
private fun inboxOrder(list: List<Incident>) = list.sortedWith(compareBy<Incident>({ if (it.severity == "critical") 0 else 1 }, { it.startedAt }))

/** The alert inbox: open alerts first, with acknowledge, mute and notes on each one. */
@Composable
fun AlertsScreen(api: VigaraApi, onOpenDevice: (String) -> Unit, onRules: () -> Unit) {
    val incidents = rememberLoad(pollMs = 30_000) { api.get<List<Incident>>("/api/alerts/incidents") }
    var bucket by remember { mutableStateOf("open") }
    var selected by remember { mutableStateOf<String?>(null) }
    val snackbar = remember { SnackbarHostState() }
    AppScaffold("Alerts", actions = { TextButton(onRules) { Text("Rules") } }, snackbar = snackbar) { padding ->
        Column(Modifier.padding(padding)) {
            val grouped = incidents.data.orEmpty().groupBy { it.bucket() }
            ChipRowWithoutAll(buckets.map { (id, label) -> id to (grouped[id]?.size?.let { "$label $it" } ?: label) }, bucket) { bucket = it }
            LoadedContent(incidents) { list ->
                val shown = inboxOrder(list.filter { it.bucket() == bucket }).let { if (bucket == "resolved") it.sortedByDescending { i -> i.resolvedAt } else it }
                if (shown.isEmpty()) EmptyState(if (bucket == "open") "No open alerts. Everything looks healthy." else "Nothing here.")
                else LazyColumn(contentPadding = PaddingValues(bottom = 16.dp)) {
                    items(shown, key = { it.id }) { incident ->
                        TwoLineRow(
                            title = incident.summary.ifBlank { incident.dimension },
                            subtitle = listOf(incident.ruleName, incident.acknowledgedBy?.let { "acknowledged by $it" }).filter { !it.isNullOrBlank() }.joinToString(" · "),
                            dot = severityColor(incident),
                            trailing = relativeTime(if (incident.status == "firing") incident.startedAt else incident.resolvedAt),
                            trailingDetail = if (incident.notes.isNotEmpty()) "${incident.notes.size} note${if (incident.notes.size == 1) "" else "s"}" else null,
                            onClick = { selected = incident.id },
                        )
                    }
                }
            }
        }
    }
    incidents.data?.firstOrNull { it.id == selected }?.let { incident ->
        IncidentSheet(api, incident, snackbar, onOpenDevice = { selected = null; onOpenDevice(it) }, onChanged = { incidents.refresh() }, onDismiss = { selected = null })
    }
}

/** A chip row where one chip is always selected. */
@Composable
private fun ChipRowWithoutAll(options: List<Pair<String, String>>, selected: String, onSelect: (String) -> Unit) {
    Row(
        Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp, vertical = 4.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        options.forEach { (value, label) -> FilterChip(selected == value, { onSelect(value) }, label = { Text(label) }) }
    }
}

@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
private fun IncidentSheet(api: VigaraApi, incident: Incident, snackbar: SnackbarHostState, onOpenDevice: (String) -> Unit, onChanged: () -> Unit, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf("") }
    val base = "/api/alerts/incidents/${incident.id}"
    val bucket = incident.bucket()

    fun act(done: String, call: suspend () -> Unit) {
        scope.launch {
            busy = true
            try { call(); onChanged(); snackbar.showSnackbar(done) }
            catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "The alert could not be updated.") }
            finally { busy = false }
        }
    }
    fun mute(minutes: Int, label: String) = act("Muted for $label.") { api.putUnit("$base/mute", buildJsonObject { put("minutes", minutes) }) }

    ModalBottomSheet(onDismissRequest = onDismiss) {
        Column(Modifier.padding(horizontal = 20.dp).padding(bottom = 16.dp).navigationBarsPadding().verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            StatusText(
                when { incident.status != "firing" -> "Resolved"; incident.severity == "critical" -> "Critical"; else -> "Warning" } +
                    when (bucket) { "acknowledged" -> " · acknowledged"; "muted" -> " · muted"; else -> "" },
                severityColor(incident),
            )
            Text(incident.summary.ifBlank { incident.dimension }, style = MaterialTheme.typography.titleMedium)
            KeyValue("Rule", incident.ruleName)
            KeyValue("Started", formatTime(incident.startedAt))
            incident.lastObservedAt?.let { KeyValue("Last seen", relativeTime(it)) }
            incident.resolvedAt?.let { KeyValue("Resolved", formatTime(it)) }
            incident.acknowledgedAt?.let { KeyValue("Acknowledged", "${relativeTime(it)}${incident.acknowledgedBy?.let { by -> " by $by" }.orEmpty()}") }
            if (bucket == "muted") KeyValue("Muted until", formatTime(incident.mutedUntil))

            if (incident.status == "firing") FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (incident.acknowledgedAt == null) Button({ act("Acknowledged.") { api.postUnit("$base/acknowledge") } }, enabled = !busy) { Text("Acknowledge") }
                else OutlinedButton({ act("Moved back to open.") { api.delete("$base/acknowledge") } }, enabled = !busy) { Text("Move to open") }
                if (bucket == "muted") OutlinedButton({ act("Unmuted.") { api.delete("$base/mute") } }, enabled = !busy) { Text("Unmute") }
                else {
                    OutlinedButton({ mute(60, "1 hour") }, enabled = !busy) { Text("Mute 1h") }
                    OutlinedButton({ mute(24 * 60, "24 hours") }, enabled = !busy) { Text("24h") }
                    OutlinedButton({ mute(7 * 24 * 60, "7 days") }, enabled = !busy) { Text("7d") }
                }
            }
            incident.deviceId?.let { id -> TextButton({ onOpenDevice(id) }) { Text("Open device") } }

            SectionTitle("Notes")
            if (incident.notes.isEmpty()) Text("No notes yet.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            incident.notes.forEach { n ->
                Column {
                    Text(n.body, style = MaterialTheme.typography.bodyMedium)
                    Text("${n.author} · ${relativeTime(n.createdAt)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            OutlinedTextField(note, { note = it }, placeholder = { Text("Add a note") }, modifier = Modifier.fillMaxWidth(), minLines = 2)
            Button({ val body = note.trim(); act("Note added.") { api.postUnit("$base/notes", buildJsonObject { put("body", body) }); note = "" } }, enabled = !busy && note.isNotBlank()) { Text("Add note") }
        }
    }
}
