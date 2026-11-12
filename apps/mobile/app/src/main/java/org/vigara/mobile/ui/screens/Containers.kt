package org.vigara.mobile.ui.screens

import android.net.Uri
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.PrimaryTabRow
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.vigara.mobile.data.ActionResult
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.AppJson
import org.vigara.mobile.data.ContainerInfo
import org.vigara.mobile.data.Fleet
import org.vigara.mobile.data.HostEntry
import org.vigara.mobile.data.InspectResult
import org.vigara.mobile.data.LogLine
import org.vigara.mobile.data.LogSnapshot
import org.vigara.mobile.data.StatsSample
import org.vigara.mobile.data.StreamEvent
import org.vigara.mobile.data.User
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.data.str
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ChipRow
import org.vigara.mobile.ui.common.ConfirmDialog
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.ItemCard
import org.vigara.mobile.ui.common.KeyValue
import org.vigara.mobile.ui.common.LineChart
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.MonoBlock
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.StatusText
import org.vigara.mobile.ui.common.TwoLineRow
import org.vigara.mobile.ui.common.formatBytes
import org.vigara.mobile.ui.common.formatEpoch
import org.vigara.mobile.ui.common.formatRate
import org.vigara.mobile.ui.common.formatTime
import org.vigara.mobile.ui.common.rememberLoad
import org.vigara.mobile.data.Series
import org.vigara.mobile.data.Point
import androidx.compose.material3.Surface

fun hostStateColor(state: String) = when (state) { "ok" -> Palette.good; "unavailable" -> Palette.warn; "error" -> Palette.bad; else -> Palette.idle }
val hostStateLabels = mapOf("ok" to "Connected", "offline" to "Offline", "unavailable" to "Docker not enabled", "error" to "Error")
val tierLabels = mapOf("read" to "Read only", "manage" to "Manage", "exec" to "Manage + terminal", "deploy" to "Full control")

fun actionsFor(state: String): List<String> = when (state) {
    "running" -> listOf("stop", "restart", "pause", "kill", "remove")
    "paused" -> listOf("unpause", "stop", "kill", "remove")
    "restarting" -> listOf("stop", "kill", "remove")
    else -> listOf("start", "remove")
}
val actionLabels = mapOf("start" to "Start", "stop" to "Stop", "restart" to "Restart", "pause" to "Pause", "unpause" to "Resume", "kill" to "Kill", "remove" to "Remove", "recreate" to "Recreate")
private val actionPast = mapOf("start" to "started", "stop" to "stopped", "restart" to "restarted", "pause" to "paused", "unpause" to "resumed", "kill" to "killed", "remove" to "removed")

suspend fun runContainerAction(api: VigaraApi, hostId: String, ref: String, action: String, force: Boolean = false, volumes: Boolean = false): ActionResult =
    api.post("/api/containers/${Uri.encode(hostId)}/${Uri.encode(ref)}/actions", buildJsonObject {
        put("action", action)
        if (action == "kill") put("signal", "KILL")
        if (action == "remove") { put("force", force); put("removeVolumes", volumes) }
    })

@Composable
fun ContainersScreen(api: VigaraApi, user: User, onOpen: (String, String) -> Unit) {
    val fleet = rememberLoad(pollMs = 10_000) { api.get<Fleet>("/api/containers").hosts }
    var search by remember { mutableStateOf("") }
    var host by remember { mutableStateOf<String?>(null) }
    var state by remember { mutableStateOf<String?>(null) }
    AppScaffold("Containers") { padding ->
        Column(Modifier.padding(padding)) {
            OutlinedTextField(
                search, { search = it }, singleLine = true, placeholder = { Text("Search name, image, project") },
                modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp),
            )
            val hosts = fleet.data.orEmpty()
            if (hosts.size > 1) ChipRow(hosts.map { it.agentId to it.name }, host, { host = it }, "All hosts")
            val states = hosts.flatMap { h -> h.containers.map { it.state } }.groupingBy { it }.eachCount()
            if (states.isNotEmpty()) ChipRow(states.entries.sortedBy { it.key }.map { it.key to "${it.key} ${it.value}" }, state, { state = it }, "All states")
            LoadedContent(fleet) { list ->
                val needle = search.trim().lowercase()
                val shown = list.filter { host == null || it.agentId == host }.map { h ->
                    h to h.containers.filter { c ->
                        (state == null || c.state == state) &&
                            (needle.isEmpty() || listOf(c.name, c.image, c.project, c.service, c.id, h.name).any { it.lowercase().contains(needle) })
                    }
                }.filter { (h, cs) -> cs.isNotEmpty() || (needle.isEmpty() && state == null && h.state != "ok") }
                if (shown.isEmpty()) EmptyState(if (list.isEmpty()) "No hosts with Docker enabled yet." else "No containers match.")
                else LazyColumn(contentPadding = PaddingValues(bottom = 16.dp)) {
                    shown.forEach { (h, cs) ->
                        item(key = "host-${h.agentId}") { HostHeader(h) }
                        items(cs, key = { "${h.agentId}/${it.id}" }) { c -> ContainerRow(c) { onOpen(h.agentId, c.name.ifBlank { c.id }) } }
                    }
                }
            }
        }
    }
}

@Composable
private fun HostHeader(host: HostEntry) {
    Column(Modifier.padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 6.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(host.name, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
            StatusText(if (host.state == "ok") tierLabels[host.dockerTier] ?: host.dockerTier else hostStateLabels[host.state] ?: host.state, hostStateColor(host.state))
        }
        if (host.message.isNotBlank()) Text(host.message, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (host.truncated) Text("Only part of this host's containers are shown.", style = MaterialTheme.typography.bodySmall, color = Palette.warn)
    }
    androidx.compose.material3.HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
}

@Composable
private fun ContainerRow(c: ContainerInfo, onClick: () -> Unit) {
    // Name and state on top; image (and stack) beneath; status text and the first published port on the right.
    val port = c.ports.firstOrNull { it.publicPort > 0 }?.let { ":${it.publicPort}" }
    TwoLineRow(
        title = c.name.ifBlank { c.id.take(12) },
        subtitle = listOf(c.image, c.project.takeIf { it.isNotBlank() }?.let { "stack $it" }).filterNotNull().joinToString(" · "),
        dot = Palette.container(c.state),
        trailing = c.state,
        trailingDetail = port ?: c.status.takeIf { c.state != "running" }?.substringBefore(" (")?.take(18),
        trailingColor = Palette.container(c.state).takeIf { c.state != "running" },
        onClick = onClick,
    )
}

@Composable
fun ContainerDetailScreen(api: VigaraApi, user: User, hostId: String, ref: String, onBack: () -> Unit) {
    val fleet = rememberLoad(hostId, pollMs = 10_000) { api.get<Fleet>("/api/containers").hosts }
    val host = fleet.data?.firstOrNull { it.agentId == hostId }
    var reveal by remember { mutableStateOf(false) }
    val inspect = rememberLoad(listOf(hostId, ref, reveal), pollMs = 15_000) {
        api.get<InspectResult>("/api/containers/${Uri.encode(hostId)}/${Uri.encode(ref)}/inspect${if (reveal) "?reveal=env" else ""}")
    }
    var tab by remember { mutableStateOf(0) }
    val snackbar = remember { SnackbarHostState() }
    val running = inspect.data?.state?.running

    AppScaffold(inspect.data?.name?.trimStart('/') ?: ref, onBack = onBack, snackbar = snackbar) { padding ->
        Column(Modifier.padding(padding).fillMaxSize()) {
            PrimaryTabRow(tab) {
                listOf("Overview", "Logs", "Stats", "Terminal").forEachIndexed { i, t -> Tab(tab == i, { tab = i }, text = { Text(t) }) }
            }
            when (tab) {
                0 -> LoadedContent(inspect) { d ->
                    ContainerOverview(api, user, host, hostId, ref, d, reveal, { reveal = it }, snackbar, onChanged = { inspect.refresh(); fleet.refresh() }, onRemoved = onBack)
                }
                1 -> ContainerLogs(api, hostId, ref)
                2 -> ContainerStats(api, hostId, ref, running)
                else -> {
                    val blocker = when {
                        !user.canOperate -> "Your account can view containers but not open terminals in them."
                        host == null -> "Loading host details…"
                        !host.canExec(user) -> "Terminals are not enabled for this host. Reinstall its agent with Docker access that includes terminals."
                        running == false -> "This container is not running, so there is nothing to open a terminal in."
                        else -> null
                    }
                    TerminalPanel(
                        api, "/api/containers/${Uri.encode(hostId)}/${Uri.encode(ref)}/exec", inspect.data?.name?.trimStart('/') ?: ref, blocker,
                        "This opens a shell inside the container. Anything you type runs with the container's privileges, which are often root.",
                        listOf("auto" to "Auto", "sh" to "sh", "bash" to "bash"), allowUser = true,
                    )
                }
            }
        }
    }
}

@Composable
private fun ContainerOverview(
    api: VigaraApi, user: User, host: HostEntry?, hostId: String, ref: String, d: InspectResult, reveal: Boolean,
    onReveal: (Boolean) -> Unit, snackbar: SnackbarHostState, onChanged: () -> Unit, onRemoved: () -> Unit,
) {
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf<String?>(null) }
    var confirm by remember { mutableStateOf<String?>(null) }
    var recreate by remember { mutableStateOf(false) }
    var force by remember { mutableStateOf(false) }
    var volumes by remember { mutableStateOf(false) }
    val mayManage = host?.canManage(user) == true
    val name = d.name.trimStart('/')

    fun run(action: String) {
        scope.launch {
            busy = action
            try {
                val result = runContainerAction(api, hostId, ref, action, force, volumes)
                snackbar.showSnackbar(result.message.ifBlank { "Container ${actionPast[action] ?: action}." })
                if (action == "remove") onRemoved() else onChanged()
            } catch (failure: ApiException) {
                snackbar.showSnackbar(failure.message ?: "The $action request failed.")
            } finally { busy = null }
        }
    }

    Column(Modifier.fillMaxSize()) {
    LazyColumn(Modifier.weight(1f), contentPadding = PaddingValues(16.dp)) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                StatusText(d.state.status.ifBlank { "unknown" }, Palette.container(d.state.status))
                if (d.state.health.isNotBlank()) StatusText(d.state.health, if (d.state.health == "healthy") Palette.good else Palette.warn)
            }
            if (!mayManage) InfoBanner(
                if (!user.canOperate) "Your account can view containers but not change them." else "Container management is not enabled for this host. Enable it from the host's Docker settings in the web UI.",
                Modifier.padding(top = 8.dp),
            )

            SectionTitle("Container")
            KeyValue("Image", d.image, mono = true)
            KeyValue("ID", d.id.take(12), mono = true)
            KeyValue("Created", formatTime(d.created))
            KeyValue("Started", formatTime(d.state.startedAt))
            if (!d.state.running) KeyValue("Finished", formatTime(d.state.finishedAt))
            KeyValue("Exit code", if (d.state.running) "" else d.state.exitCode.toString())
            KeyValue("Restarts", d.restartCount.toString())
            KeyValue("Restart policy", d.restartPolicy)
            KeyValue("Command", (d.entrypoint + d.command).joinToString(" "), mono = true)
            KeyValue("Working dir", d.workingDir, mono = true)
            KeyValue("User", d.user)
            if (d.state.oomKilled) ErrorBanner("The container was killed for running out of memory.")
            if (d.state.error.isNotBlank()) ErrorBanner(d.state.error)
            if (d.privileged) ErrorBanner("This container runs in privileged mode.", Modifier.padding(top = 6.dp))

            if (d.ports.isNotEmpty()) {
                SectionTitle("Ports")
                d.ports.distinctBy { Triple(it.ip, it.publicPort, it.privatePort) }.forEach {
                    KeyValue(it.type.uppercase(), if (it.publicPort > 0) "${it.ip.ifBlank { "0.0.0.0" }}:${it.publicPort} → ${it.privatePort}" else "${it.privatePort} (not published)", mono = true)
                }
            }
            if (d.networks.isNotEmpty()) {
                SectionTitle("Networks")
                d.networks.forEach { KeyValue(it.name, listOf(it.ipAddress, it.gateway.takeIf { g -> g.isNotBlank() }?.let { g -> "gw $g" }).filterNotNull().joinToString("  "), mono = true) }
            }
            if (d.mounts.isNotEmpty()) {
                SectionTitle("Mounts")
                d.mounts.forEach { KeyValue(d.mounts.indexOf(it).let { _ -> it.type }, "${it.name.ifBlank { it.source }} → ${it.destination}${if (it.readWrite) "" else " (ro)"}", mono = true) }
            }
            SectionTitle("Environment")
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("Show secret values", Modifier.weight(1f))
                Switch(reveal, onReveal)
            }
            if (d.env.isEmpty()) Text("No environment variables.", color = MaterialTheme.colorScheme.onSurfaceVariant)
            else MonoBlock(d.env.joinToString("\n") { "${it.name}=${if (reveal || d.envRevealed) it.value.orEmpty() else (it.value ?: "••••••••")}" })
            if (d.labels.isNotEmpty()) {
                SectionTitle("Labels")
                MonoBlock(d.labels.entries.sortedBy { it.key }.joinToString("\n") { "${it.key}=${it.value}" })
            }
        }
    }
    if (mayManage) ContainerActionBar(actionsFor(d.state.status.ifBlank { "exited" }), busy, onAction = { a ->
        if (a == "recreate") recreate = true
        else if (a == "remove" || a == "kill" || a == "stop" || a == "restart") confirm = a
        else run(a)
    })
    }

    confirm?.let { action ->
        ConfirmDialog(
            title = "${actionLabels[action]} $name?",
            text = when (action) {
                "remove" -> "The container will be deleted. Data in named volumes is kept unless you also remove volumes."
                "kill" -> "The main process is sent SIGKILL immediately, without a clean shutdown."
                "stop" -> "The container is stopped and any running work is interrupted."
                else -> "The container restarts and is briefly unavailable."
            },
            confirmLabel = actionLabels[action] ?: action,
            destructive = action == "remove" || action == "kill",
            onConfirm = { confirm = null; run(action) },
            onDismiss = { confirm = null },
            extra = {
                if (action == "remove") Column {
                    Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(force, { force = it }); Text("Force (even if running)") }
                    Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(volumes, { volumes = it }); Text("Also remove its volumes") }
                }
            },
        )
    }
    if (recreate) RecreateDialog(api, hostId, ref, name, snackbar, onDone = { recreate = false; onChanged() }, onDismiss = { recreate = false })
}

@Composable
private fun RecreateDialog(api: VigaraApi, hostId: String, ref: String, name: String, snackbar: SnackbarHostState, onDone: () -> Unit, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    var pull by remember { mutableStateOf(true) }
    var running by remember { mutableStateOf(false) }
    var finished by remember { mutableStateOf(false) }
    val steps = remember { mutableStateListOf<String>() }
    var error by remember { mutableStateOf<String?>(null) }

    androidx.compose.material3.AlertDialog(
        onDismissRequest = { if (!running) onDismiss() },
        title = { Text("Recreate $name") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("A new container is created with the same configuration and the old one is replaced. It is rolled back if the new one fails to start.")
                if (!running && !finished) Row(verticalAlignment = Alignment.CenterVertically) { Checkbox(pull, { pull = it }); Text("Pull the latest image first") }
                steps.forEach { Text(it, style = MaterialTheme.typography.bodySmall) }
                error?.let { ErrorBanner(it) }
                if (running) Spinner()
            }
        },
        confirmButton = {
            if (finished) androidx.compose.material3.TextButton(onDone) { Text("Done") }
            else Button(enabled = !running, onClick = {
                scope.launch {
                    running = true; error = null; steps.clear()
                    try {
                        api.postLines("/api/containers/${Uri.encode(hostId)}/${Uri.encode(ref)}/recreate", buildJsonObject { put("pull", pull) }) { line ->
                            val step = line.str("step").orEmpty()
                            when (step) {
                                "error" -> error = line.str("message") ?: "The recreate failed."
                                "done" -> steps += "Done"
                                else -> steps += listOf(step, line.str("message")).filterNotNull().filter { it.isNotBlank() }.joinToString(": ")
                            }
                        }
                        finished = error == null
                    } catch (failure: ApiException) {
                        error = failure.message
                    } finally { running = false }
                }
            }) { Text("Recreate") }
        },
        dismissButton = { if (!running && !finished) androidx.compose.material3.TextButton(onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun ContainerLogs(api: VigaraApi, hostId: String, ref: String) {
    var follow by remember { mutableStateOf(false) }
    var timestamps by remember { mutableStateOf(false) }
    var search by remember { mutableStateOf("") }
    var tail by remember { mutableStateOf(200) }
    val lines = remember { mutableStateListOf<LogLine>() }
    var status by remember { mutableStateOf("") }
    var truncated by remember { mutableStateOf(false) }
    var failure by remember { mutableStateOf<String?>(null) }
    var reload by remember { mutableStateOf(0) }
    val base = "/api/containers/${Uri.encode(hostId)}/${Uri.encode(ref)}/logs"

    LaunchedEffect(follow, timestamps, tail, reload) {
        lines.clear(); failure = null; truncated = false
        if (!follow) {
            status = "Loading…"
            try {
                val snap = api.get<LogSnapshot>("$base/snapshot?tail=$tail&timestamps=$timestamps")
                lines.addAll(snap.lines); truncated = snap.truncated; status = "${snap.lines.size} lines"
            } catch (e: ApiException) { failure = e.message; status = "" }
        } else {
            status = "Connecting…"
            api.stream("$base?tail=$tail&follow=true&timestamps=$timestamps").collect { event ->
                when (event) {
                    StreamEvent.Open -> status = "Live"
                    is StreamEvent.Records -> {
                        for (r in event.records) {
                            if (r["end"]?.jsonPrimitive?.boolean == true) { status = "Stream ended"; continue }
                            lines += AppJson.decodeFromJsonElement(LogLine.serializer(), r)
                        }
                        while (lines.size > 2000) lines.removeAt(0)
                    }
                    is StreamEvent.Closed -> status = "Stream ended"
                    is StreamEvent.Failed -> { status = ""; failure = event.message }
                }
            }
        }
    }

    val needle = search.trim().lowercase()
    val shown = if (needle.isEmpty()) lines.toList() else lines.filter { it.m.lowercase().contains(needle) }
    val listState = rememberLazyListState()
    LaunchedEffect(shown.size, follow) { if (follow && shown.isNotEmpty()) listState.scrollToItem(shown.lastIndex) }

    Column(Modifier.fillMaxSize()) {
        Row(Modifier.padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically) {
            OutlinedTextField(search, { search = it }, singleLine = true, placeholder = { Text("Filter") }, modifier = Modifier.weight(1f))
        }
        Row(Modifier.padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            Switch(follow, { follow = it }); Text("Live", style = MaterialTheme.typography.bodySmall)
            Switch(timestamps, { timestamps = it }); Text("Time", style = MaterialTheme.typography.bodySmall)
            Text(status, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
            if (!follow) androidx.compose.material3.TextButton({ tail = if (tail >= 1000) 200 else tail * 5; reload++ }) { Text("Tail $tail") }
            androidx.compose.material3.TextButton({ reload++ }) { Text("Reload") }
        }
        failure?.let { ErrorBanner(it) }
        if (truncated) InfoBanner("Output was truncated; showing the newest lines.", Modifier.padding(horizontal = 12.dp))
        Surface(color = MaterialTheme.colorScheme.surfaceContainerHighest, modifier = Modifier.weight(1f).fillMaxWidth().padding(8.dp)) {
            SelectionContainer {
                LazyColumn(state = listState, contentPadding = PaddingValues(6.dp)) {
                    items(shown.size) { i ->
                        val l = shown[i]
                        Text(
                            (if (l.t.isNotBlank()) l.t.substringAfter('T').take(12) + " " else "") + l.m,
                            fontFamily = FontFamily.Monospace, fontSize = 11.sp, lineHeight = 14.sp,
                            color = if (l.s == "e") Palette.bad else MaterialTheme.colorScheme.onSurface,
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun ContainerStats(api: VigaraApi, hostId: String, ref: String, running: Boolean?) {
    if (running == false) { EmptyState("This container is not running. Live resource use is only available while it runs."); return }
    val samples = remember { mutableStateListOf<StatsSample>() }
    var status by remember { mutableStateOf("Connecting…") }
    var failure by remember { mutableStateOf<String?>(null) }
    var attempt by remember { mutableStateOf(0) }

    LaunchedEffect(running, attempt) {
        if (running == null) return@LaunchedEffect
        failure = null; status = "Connecting…"
        api.stream("/api/containers/${Uri.encode(hostId)}/${Uri.encode(ref)}/stats").collect { event ->
            when (event) {
                StreamEvent.Open -> status = "Live"
                is StreamEvent.Records -> {
                    for (r in event.records) {
                        if (r["end"]?.jsonPrimitive?.boolean == true) { status = "Stream ended"; continue }
                        samples += AppJson.decodeFromJsonElement(StatsSample.serializer(), r)
                    }
                    while (samples.size > 120) samples.removeAt(0)
                }
                is StreamEvent.Closed -> status = "Stream ended"
                is StreamEvent.Failed -> { failure = event.message; status = "" }
            }
        }
    }
    val last = samples.lastOrNull()
    LazyColumn(contentPadding = PaddingValues(16.dp)) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusChip(status.ifBlank { "Stopped" }, if (status == "Live") Palette.good else Palette.idle)
                androidx.compose.material3.TextButton({ attempt++ }) { Text("Reconnect") }
            }
            failure?.let { ErrorBanner(it) }
            KeyValue("CPU", last?.let { "%.1f%% of one core (%d available)".format(it.cpu, it.cpuCores) } ?: "–")
            KeyValue("Memory", last?.let { "${formatBytes(it.memUsed)} of ${formatBytes(it.memLimit)} (%.1f%%)".format(it.memPct) } ?: "–")
            KeyValue("Network", last?.let { "↓ ${formatRate(it.netRxBps)}   ↑ ${formatRate(it.netTxBps)}" } ?: "–")
            KeyValue("Disk I/O", last?.let { "read ${formatBytes(it.blkRead)} / write ${formatBytes(it.blkWrite)}" } ?: "–")
            KeyValue("Processes", last?.pids?.toString().orEmpty())
            if (samples.size > 1) {
                SectionTitle("CPU (% of one core)")
                LineChart(listOf(Series("cpu", "%", emptyMap(), samples.map { Point(it.t, it.cpu) })), "%")
                SectionTitle("Memory (% of limit)")
                LineChart(listOf(Series("mem", "%", emptyMap(), samples.map { Point(it.t, it.memPct) })), "%")
            }
        }
    }
}

/**
 * The container's actions, pinned to the bottom of the Overview tab so they stay in reach while the
 * details scroll. The two most likely actions are buttons; the rest, and Recreate, are in a menu.
 */
@Composable
private fun ContainerActionBar(actions: List<String>, busy: String?, onAction: (String) -> Unit) {
    var menu by remember { mutableStateOf(false) }
    val inline = actions.filter { it != "remove" && it != "kill" }.take(2)
    val rest = actions.filter { it !in inline } + "recreate"
    Surface(color = MaterialTheme.colorScheme.surface, shadowElevation = 6.dp) {
        Column {
            androidx.compose.material3.HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
            Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                inline.forEachIndexed { i, a ->
                    val label = @Composable { if (busy == a) Spinner() else Text(actionLabels[a] ?: a) }
                    if (i == 0) Button({ onAction(a) }, enabled = busy == null, modifier = Modifier.weight(1f)) { label() }
                    else OutlinedButton({ onAction(a) }, enabled = busy == null, modifier = Modifier.weight(1f)) { label() }
                }
                androidx.compose.foundation.layout.Box {
                    OutlinedButton({ menu = true }, enabled = busy == null) { if (busy != null && busy !in inline) Spinner() else Text("More") }
                    androidx.compose.material3.DropdownMenu(menu, { menu = false }) {
                        rest.forEach { a ->
                            androidx.compose.material3.DropdownMenuItem(
                                text = { Text(if (a == "recreate") "Recreate…" else actionLabels[a] ?: a, color = if (a == "remove" || a == "kill") Palette.bad else Color.Unspecified) },
                                onClick = { menu = false; onAction(a) },
                            )
                        }
                    }
                }
            }
        }
    }
}
