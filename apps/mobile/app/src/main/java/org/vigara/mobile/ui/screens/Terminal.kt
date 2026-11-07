package org.vigara.mobile.ui.screens

import android.net.Uri
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.AssistChip
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.vigara.mobile.data.TerminalListener
import org.vigara.mobile.data.TerminalSocket
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.TerminalBuffer

private val ansi = listOf(
    Color(0xFF2E3436), Color(0xFFCC4444), Color(0xFF4EC261), Color(0xFFD7B74A), Color(0xFF5A8FE0), Color(0xFFB36AD1), Color(0xFF3FC1C9), Color(0xFFD3D7CF),
    Color(0xFF6B7280), Color(0xFFFF6B6B), Color(0xFF7CE38B), Color(0xFFF5D76E), Color(0xFF82B1FF), Color(0xFFD99AF0), Color(0xFF7FE3E8), Color(0xFFFFFFFF),
)
private val termBg = Color(0xFF0B0D10)
private val termFg = Color(0xFFD8DEE9)

private enum class TermStatus { IDLE, CONNECTING, LIVE, ENDED, FAILED }

private const val IDLE_MINUTES = 15

/**
 * An interactive shell over the server's terminal WebSocket (container exec or device shell).
 * Input is typed in a field and sent with Enter; common control keys have buttons. Full-screen
 * programs such as vim or top do not render, but shells, logs, package managers and most CLI tools do.
 */
@Composable
fun TerminalPanel(
    api: VigaraApi,
    basePath: String,
    name: String,
    blocker: String?,
    warning: String,
    shells: List<Pair<String, String>>,
    allowUser: Boolean,
) {
    if (blocker != null) { InfoBanner(blocker, Modifier.padding(16.dp)); return }

    var shell by remember { mutableStateOf("auto") }
    var user by remember { mutableStateOf("") }
    var status by remember { mutableStateOf(TermStatus.IDLE) }
    var message by remember { mutableStateOf("") }
    var input by remember { mutableStateOf("") }
    val version = remember { mutableIntStateOf(0) }
    val buffer = remember { TerminalBuffer() }
    val socket = remember { arrayOfNulls<TerminalSocket>(1) }
    var cols by remember { mutableIntStateOf(80) }
    var rows by remember { mutableIntStateOf(24) }

    fun close() { socket[0]?.close(); socket[0] = null }
    DisposableEffect(Unit) { onDispose { close() } }

    val userValid = user.isBlank() || Regex("^[A-Za-z0-9_.-]{1,64}(:[A-Za-z0-9_.-]{1,64})?$").matches(user.trim())

    fun connect() {
        close(); buffer.clear(); buffer.cols = cols; message = ""; status = TermStatus.CONNECTING
        val query = buildString {
            if (shell != "auto") append("shell=").append(Uri.encode(shell)).append('&')
            if (allowUser && user.isNotBlank()) append("user=").append(Uri.encode(user.trim())).append('&')
            append("cols=").append(cols.coerceIn(10, 500)).append("&rows=").append(rows.coerceIn(10, 500))
        }
        socket[0] = api.openTerminal("$basePath?$query", object : TerminalListener {
            override fun onOpen() { status = TermStatus.LIVE; socket[0]?.resize(cols, rows) }
            override fun onOutput(bytes: ByteArray) { buffer.feed(bytes); version.intValue++ }
            override fun onText(text: String) {
                val end = runCatching { Json.parseToJsonElement(text).jsonObject }.getOrNull()
                val ended = end?.get("end")?.jsonPrimitive?.boolean == true
                message = (end?.get("message")?.jsonPrimitive?.content).orEmpty().ifBlank { if (ended) "The shell exited." else "The terminal session ended." }
                buffer.feed("\r\n[$message]\r\n".toByteArray()); version.intValue++
                status = TermStatus.ENDED
            }
            override fun onClosed(error: String?) {
                if (status == TermStatus.ENDED) return
                message = error ?: "The connection to the server was lost."
                status = TermStatus.FAILED
            }
        })
    }

    fun send(text: String) { socket[0]?.send(text.toByteArray()) }

    val active = status == TermStatus.CONNECTING || status == TermStatus.LIVE
    Column(Modifier.fillMaxSize()) {
        if (!active) {
            Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                InfoBanner("$warning The session closes after $IDLE_MINUTES minutes without typing, and when you leave this tab.")
                Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    shells.forEach { (v, l) -> FilterChip(shell == v, { shell = v }, label = { Text(l) }) }
                }
                if (allowUser) OutlinedTextField(
                    user, { user = it }, label = { Text("Run as (optional)") }, singleLine = true, isError = !userValid,
                    supportingText = { if (!userValid) Text("Use a user name or number, optionally followed by :group.") },
                    modifier = Modifier.fillMaxWidth(),
                )
                Button({ connect() }, enabled = userValid) { Text(if (status == TermStatus.IDLE) "Open terminal" else "Reconnect") }
                if (status == TermStatus.FAILED) Text(message, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
            }
        } else {
            Row(Modifier.padding(horizontal = 12.dp, vertical = 4.dp), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
                StatusChip(if (status == TermStatus.LIVE) "Connected" else "Connecting…", if (status == TermStatus.LIVE) Palette.good else Palette.info)
                Text(" $name", Modifier.weight(1f), style = MaterialTheme.typography.labelMedium, maxLines = 1)
                OutlinedButton({ close(); status = TermStatus.IDLE }) { Text("Disconnect") }
            }
        }

        val measurer = rememberTextMeasurer()
        val style = remember { TextStyle(fontFamily = FontFamily.Monospace, fontSize = 12.sp, lineHeight = 16.sp) }
        val charPx = remember { measurer.measure("M", style).size }
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth().background(termBg).padding(6.dp)) {
            val wPx = with(androidx.compose.ui.platform.LocalDensity.current) { maxWidth.toPx() }
            val hPx = with(androidx.compose.ui.platform.LocalDensity.current) { maxHeight.toPx() }
            val newCols = (wPx / charPx.width).toInt().coerceIn(20, 300)
            val newRows = (hPx / charPx.height).toInt().coerceIn(5, 200)
            LaunchedEffect(newCols, newRows) {
                cols = newCols; rows = newRows; buffer.cols = newCols
                if (status == TermStatus.LIVE) socket[0]?.resize(newCols, newRows)
            }
            val lines = remember(version.intValue) { buffer.snapshot().takeLast(600) }
            val listState = rememberLazyListState()
            LaunchedEffect(lines.size, version.intValue) { if (lines.isNotEmpty()) listState.scrollToItem(lines.lastIndex) }
            LazyColumn(state = listState, modifier = Modifier.fillMaxSize()) {
                items(lines.size) { i ->
                    Text(render(lines[i]), style = style, color = termFg, softWrap = false)
                }
            }
        }

        if (status == TermStatus.LIVE) {
            Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                listOf("Tab" to "\t", "Esc" to "\u001b", "^C" to "\u0003", "^D" to "\u0004", "^L" to "\u000c", "↑" to "\u001b[A", "↓" to "\u001b[B", "←" to "\u001b[D", "→" to "\u001b[C").forEach { (label, seq) ->
                    AssistChip({ send(seq) }, label = { Text(label) })
                }
            }
            OutlinedTextField(
                input, { input = it }, singleLine = true, placeholder = { Text("Type a command, then Enter") },
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Ascii, imeAction = ImeAction.Send, autoCorrectEnabled = false),
                keyboardActions = KeyboardActions(onSend = { send(input + "\r"); input = "" }),
                textStyle = TextStyle(fontFamily = FontFamily.Monospace),
                modifier = Modifier.fillMaxWidth().padding(8.dp),
            )
        }
    }
}

private fun render(cells: List<org.vigara.mobile.ui.common.Cell>): AnnotatedString = buildAnnotatedString {
    if (cells.isEmpty()) { append(" "); return@buildAnnotatedString }
    var i = 0
    while (i < cells.size) {
        var j = i
        val fg = cells[i].fg
        val bold = cells[i].bold
        while (j < cells.size && cells[j].fg == fg && cells[j].bold == bold) j++
        val text = String(CharArray(j - i) { cells[i + it].ch })
        if (fg < 0 && !bold) append(text)
        else withStyle(SpanStyle(color = if (fg in 0..15) ansi[if (bold && fg < 8) fg + 8 else fg] else termFg, fontWeight = if (bold) FontWeight.Bold else null)) { append(text) }
        i = j
    }
}
