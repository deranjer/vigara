package org.vigara.mobile.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.SnackbarHostState
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
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.UpdateItem
import org.vigara.mobile.data.UpdateOverview
import org.vigara.mobile.data.User
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ChipRow
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.ItemCard
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.formatTime
import org.vigara.mobile.ui.common.rememberLoad

private fun statusColor(status: String) = when (status) {
    "update-available", "update_available", "outdated" -> Palette.warn
    "up-to-date", "up_to_date", "current" -> Palette.good
    "error", "failed" -> Palette.bad
    else -> Palette.idle
}

@Composable
fun ImageUpdatesScreen(api: VigaraApi, user: User, onOpenContainer: (String, String) -> Unit) {
    val overview = rememberLoad(pollMs = 30_000) { api.get<UpdateOverview>("/api/image-updates") }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    var filter by remember { mutableStateOf<String?>(null) }
    AppScaffold(
        "Image updates", snackbar = snackbar,
        actions = {
            if (user.canOperate) TextButton({
                scope.launch {
                    try { api.postUnit("/api/image-updates/check", buildJsonObject { }); snackbar.showSnackbar("Image checks queued. Results refresh here shortly.") }
                    catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "Could not queue checks.") }
                    overview.refresh()
                }
            }) { Text("Check all") }
        },
    ) { padding ->
        Column(Modifier.padding(padding)) {
            val items = overview.data?.items.orEmpty()
            val statuses = items.map { it.result.status }.groupingBy { it }.eachCount()
            if (statuses.isNotEmpty()) ChipRow(statuses.entries.sortedBy { it.key }.map { it.key to "${it.key} ${it.value}" }, filter, { filter = it }, "All")
            LoadedContent(overview) { data ->
                val shown = data.items.filter { filter == null || it.result.status == filter }
                    .sortedWith(compareBy<UpdateItem> { it.result.status != "update-available" && it.result.status != "update_available" }.thenBy { it.result.name })
                if (shown.isEmpty() && data.hosts.isEmpty()) EmptyState("No image checks yet. Run \"Check all\" to start.")
                else LazyColumn(contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    items(shown, key = { "${it.agentId}/${it.containerId}" }) { item ->
                        ItemCard(onClick = { onOpenContainer(item.agentId, item.containerId) }) {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text(item.result.name.ifBlank { item.containerId.take(12) }, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                                StatusChip(item.result.status.ifBlank { "unknown" }, statusColor(item.result.status))
                            }
                            Text("${item.device} · ${item.result.image}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            if (item.result.available.isNotBlank()) Text("Available: ${item.result.available.take(19)}", style = MaterialTheme.typography.bodySmall)
                            if (item.result.message.isNotBlank()) Text(item.result.message, style = MaterialTheme.typography.bodySmall)
                            Text("Checked ${formatTime(item.checkedAt)}${if (item.ignored) " · ignored" else ""}", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                    if (data.hosts.isNotEmpty()) {
                        item { SectionTitle("Hosts") }
                        items(data.hosts, key = { "h-${it.agentId}" }) { h ->
                            ItemCard {
                                Text(h.device, style = MaterialTheme.typography.titleSmall)
                                Text("Last attempt ${formatTime(h.checkedAt, "never")} · next ${formatTime(h.nextCheck)}", style = MaterialTheme.typography.bodySmall)
                                if (h.message.isNotBlank()) Text("${h.message} Existing results may be stale.", style = MaterialTheme.typography.bodySmall, color = Palette.warn)
                            }
                        }
                    }
                }
            }
        }
    }
}
