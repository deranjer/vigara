package org.vigara.mobile.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.CreatedStack
import org.vigara.mobile.data.DraftResult
import org.vigara.mobile.data.Encrypted
import org.vigara.mobile.data.Revealed
import org.vigara.mobile.data.SaveResult
import org.vigara.mobile.data.SetupDevices
import org.vigara.mobile.data.StackDetail
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.ConfirmDialog
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.rememberDownloader

fun starterFiles(id: String, project: String): Map<String, String> = mapOf(
    "vigara-stack.yaml" to "apiVersion: vigara.io/v1\nkind: Stack\nstackId: $id\ndisplayName: $id\nprojectName: $project\ncompose:\n  files:\n    - compose.yaml\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n",
    "compose.yaml" to "services:\n  web:\n    image: nginx:stable\n    ports:\n      - \"8080:80\"\n",
)

fun isEncrypted(path: String) = path.endsWith(".age") || path.endsWith(".sops")

/** The editable working copy of a stack: files, the stored draft's optimistic-lock version, and Git state. */
@Stable
class StackEditState(private val api: VigaraApi, private val id: String, val base: String) {
    var files by mutableStateOf<Map<String, String>>(emptyMap())
    var savedFiles by mutableStateOf<Map<String, String>>(emptyMap())
    var draftFiles by mutableStateOf<Map<String, String>?>(null)
    var draftVersion by mutableStateOf(0)
    var draftBase by mutableStateOf("")
    var commit by mutableStateOf("")
    var branch by mutableStateOf("")
    var assigned by mutableStateOf(false)
    var projectName by mutableStateOf("")
    var loaded by mutableStateOf(false)
    var busy by mutableStateOf(false)
    var error by mutableStateOf<String?>(null)
    var notice by mutableStateOf<String?>(null)
    /** Set while a secret is on screen so the working copy is not reloaded underneath it. */
    var secretOpen by mutableStateOf(false)

    val dirty get() = files != savedFiles
    val unpersisted get() = draftFiles?.let { files != it } ?: dirty

    suspend fun load() {
        val d = api.get<StackDetail>(base)
        savedFiles = d.snapshot.files; commit = d.snapshot.commit; branch = d.branch; assigned = d.assigned; projectName = d.projectName
        draftFiles = d.draft.files; draftVersion = d.draft.version; draftBase = d.draft.base
        files = d.draft.files ?: d.snapshot.files.ifEmpty { starterFiles(id, d.projectName.ifBlank { id }) }
        loaded = true
    }

    suspend fun persist(next: Map<String, String> = files): DraftResult {
        val r = api.put<DraftResult>("$base/draft", buildJsonObject {
            put("base", draftBase.ifBlank { commit }); put("version", draftVersion)
            put("files", JsonObject(next.mapValues { JsonPrimitive(it.value) }))
        })
        draftVersion = r.version; draftBase = r.base; draftFiles = next
        return r
    }

    suspend fun save(): SaveResult {
        val d = persist()
        val result = api.post<SaveResult>("$base/save", buildJsonObject { put("version", d.version) })
        if (result.state != "pendingPush") load()
        return result
    }

    suspend fun discard() {
        if (draftFiles != null) api.delete("$base/draft", buildJsonObject { put("version", draftVersion) })
        load()
    }

    /** Runs [work] with the busy flag and surfaces failures; returns true on success. */
    suspend fun run(work: suspend () -> Unit): Boolean {
        busy = true; error = null; notice = null
        return try { work(); true } catch (e: ApiException) { error = e.message; false } finally { busy = false }
    }
}

@Composable
fun StackFilesTab(state: StackEditState, snackbar: SnackbarHostState, canEdit: Boolean) {
    val scope = rememberCoroutineScope()
    var selected by remember { mutableStateOf<String?>(null) }
    var adding by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf(false) }
    var discarding by remember { mutableStateOf(false) }
    val names = state.files.keys.sorted()
    val current = selected?.takeIf { it in state.files } ?: names.firstOrNull { it.startsWith("compose") } ?: names.firstOrNull()

    if (!state.loaded) { Spinner(Modifier.padding(16.dp)); return }
    Column(Modifier.fillMaxSize().padding(horizontal = 12.dp)) {
        state.error?.let { ErrorBanner(it) }
        state.notice?.let { InfoBanner(it, Modifier.padding(vertical = 4.dp)) }
        if (!canEdit) InfoBanner("Your account can view stack files but not edit them.", Modifier.padding(vertical = 4.dp))
        Row(Modifier.horizontalScroll(rememberScrollState()).padding(vertical = 4.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            names.forEach { n -> FilterChip(current == n, { selected = n }, label = { Text(n, maxLines = 1) }) }
            if (canEdit) TextButton({ adding = true }) { Text("+ File") }
        }
        if (current == null) EmptyState("No files.")
        else if (isEncrypted(current)) InfoBanner("$current is encrypted. Use the Env tab to reveal or replace its values.")
        else OutlinedTextField(
            state.files[current].orEmpty(), { v -> if (canEdit) state.files = state.files + (current to v) },
            readOnly = !canEdit, modifier = Modifier.fillMaxWidth().weight(1f),
            textStyle = TextStyle(fontFamily = FontFamily.Monospace, fontSize = 12.sp, lineHeight = 16.sp),
            keyboardOptions = androidx.compose.foundation.text.KeyboardOptions(autoCorrectEnabled = false, keyboardType = KeyboardType.Ascii),
        )
        if (canEdit) {
            Row(Modifier.horizontalScroll(rememberScrollState()).padding(vertical = 6.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                OutlinedButton({ scope.launch { if (state.run { state.persist() }) snackbar.showSnackbar("Draft stored. Git and running services are unchanged.") } }, enabled = !state.busy && state.unpersisted) { Text("Keep draft") }
                Button({
                    scope.launch {
                        var pending = false
                        if (state.run { pending = state.save().state == "pendingPush" }) {
                            snackbar.showSnackbar(if (pending) "Pending sync: the draft is stored but Git has not confirmed the push. Save again to retry." else "Saved to ${state.branch}. Running services are unchanged.")
                        }
                    }
                }, enabled = !state.busy && state.dirty && state.assigned) { if (state.busy) Spinner() else Text("Save to ${state.branch.ifBlank { "Git" }}") }
                TextButton({ deleting = true }, enabled = !state.busy && current != null && current != "vigara-stack.yaml") { Text("Delete file") }
                TextButton({ discarding = true }, enabled = !state.busy && (state.dirty || state.draftFiles != null)) { Text("Discard draft", color = MaterialTheme.colorScheme.error) }
            }
            if (!state.assigned) Text("Assign this stack to a device in the web UI before saving.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }

    if (adding) {
        var name by remember { mutableStateOf("") }
        val valid = name.isNotBlank() && !name.startsWith("/") && !name.contains("..") && name !in state.files
        AlertDialog(
            onDismissRequest = { adding = false }, title = { Text("New file") },
            text = { OutlinedTextField(name, { name = it }, label = { Text("Path, for example config/app.env") }, singleLine = true, isError = name.isNotEmpty() && !valid) },
            confirmButton = { Button({ state.files = state.files + (name.trim() to ""); selected = name.trim(); adding = false }, enabled = valid) { Text("Add") } },
            dismissButton = { TextButton({ adding = false }) { Text("Cancel") } },
        )
    }
    if (deleting && current != null) ConfirmDialog("Delete $current?", "The file is removed from your draft. Git history keeps earlier revisions.", "Delete", destructive = true,
        onConfirm = { state.files = state.files - current; selected = null; deleting = false }, onDismiss = { deleting = false })
    if (discarding) ConfirmDialog("Discard draft?", "Your edits and the stored draft are discarded. Saved Git revisions and running services are kept.", "Discard", destructive = true,
        onConfirm = { discarding = false; scope.launch { state.run { state.discard() } } }, onDismiss = { discarding = false })
}

@Composable
fun StackEnvTab(api: VigaraApi, state: StackEditState, canEdit: Boolean, snackbar: SnackbarHostState) {
    val scope = rememberCoroutineScope()
    val encrypted = state.files.keys.filter { isEncrypted(it) }.sorted()
    var path by remember(encrypted.toString()) { mutableStateOf(encrypted.firstOrNull() ?: "secrets/runtime.env.age") }
    var value by remember { mutableStateOf("") }
    var editing by remember { mutableStateOf(false) }
    var source by remember { mutableStateOf<DraftResult?>(null) }
    val download = rememberDownloader(api) { scope.launch { snackbar.showSnackbar(it) } }

    fun hide() { value = ""; editing = false; source = null; state.secretOpen = false }
    // Plaintext must never outlive the screen.
    DisposableEffect(Unit) { onDispose { value = ""; state.secretOpen = false } }
    LaunchedEffect(state.commit) { hide() }

    if (!state.loaded) { Spinner(Modifier.padding(16.dp)); return }
    val base = state.base
    LazyColumn(contentPadding = PaddingValues(16.dp)) {
        item {
            Text("Encrypted environment", style = MaterialTheme.typography.titleMedium)
            Text(
                "Values are hidden. Reveal decrypts your exact draft on the server and is audited. Encrypting adds ciphertext to the draft; save the revision when ready. First use sets up age encryption and needs the device online.",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(vertical = 6.dp),
            )
            state.error?.let { ErrorBanner(it) }
            OutlinedButton({ download("GET", "/api/stacks/imports/recovery", "vigara-recovery-key.txt", null, null) }, enabled = encrypted.any { it.endsWith(".age") }) { Text("Download recovery key") }
            SectionTitle("Encrypted file")
            OutlinedTextField(path, { hide(); path = it }, singleLine = true, enabled = !state.busy && canEdit, modifier = Modifier.fillMaxWidth())
            Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                encrypted.forEach { f -> FilterChip(f == path, { hide(); path = f }, label = { Text(f) }) }
            }
            if (!canEdit) InfoBanner("Your account cannot edit environment values.", Modifier.padding(top = 8.dp))
            if (editing) {
                OutlinedTextField(
                    value, { value = it }, label = { Text(if (source != null) "Revealed dotenv contents" else "Replacement dotenv contents") },
                    modifier = Modifier.fillMaxWidth().heightIn(min = 240.dp).padding(top = 8.dp),
                    textStyle = TextStyle(fontFamily = FontFamily.Monospace, fontSize = 12.sp),
                    keyboardOptions = androidx.compose.foundation.text.KeyboardOptions(autoCorrectEnabled = false, keyboardType = KeyboardType.Password),
                )
                Text("Hiding or leaving this tab clears the form. Encrypting clears it even if the request fails.", style = MaterialTheme.typography.bodySmall)
                Row(Modifier.padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button({
                        val plaintext = value; val src = source
                        hide()
                        scope.launch {
                            state.run {
                                val exact = src ?: state.persist()
                                val result = api.post<Encrypted>("$base/environment", buildJsonObject {
                                    put("path", path); put("source", buildJsonObject { put("version", exact.version); put("digest", exact.digest) }); put("value", plaintext)
                                })
                                path = result.path
                                state.files = state.files + (result.path to result.ciphertext) + ("vigara-stack.yaml" to result.manifest)
                                snackbar.showSnackbar("Environment encrypted into your draft. Save the revision when ready.")
                            }
                        }
                    }, enabled = !state.busy) { Text("Encrypt into draft") }
                    OutlinedButton({ hide() }) { Text("Hide and clear") }
                }
            } else if (canEdit) {
                Row(Modifier.padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button({
                        scope.launch {
                            state.run {
                                val d = state.persist()
                                val r = api.post<Revealed>("$base/environment/reveal", buildJsonObject {
                                    put("path", path); put("source", buildJsonObject { put("version", d.version); put("digest", d.digest) })
                                })
                                source = d; value = r.value; editing = true; state.secretOpen = true
                            }
                        }
                    }, enabled = !state.busy && state.files.containsKey(path)) { if (state.busy) Spinner() else Text("Reveal and edit") }
                    OutlinedButton({ hide(); editing = true; state.secretOpen = true }, enabled = !state.busy) { Text("Enter replacement") }
                }
            }
        }
    }
}

@Composable
fun CreateStackDialog(api: VigaraApi, onDone: (String?) -> Unit) {
    val devices = org.vigara.mobile.ui.common.rememberLoad { api.get<SetupDevices>("/api/stacks/setup").devices }
    var name by remember { mutableStateOf("") }
    var agent by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val slug = name.lowercase().replace(Regex("[^a-z0-9]+"), "-").trim('-').take(63)
    AlertDialog(
        onDismissRequest = { if (!busy) onDone(null) }, title = { Text("New stack") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("Creates a draft; no containers start until you save and deploy.", style = MaterialTheme.typography.bodySmall)
                error?.let { ErrorBanner(it) }
                OutlinedTextField(name, { name = it }, label = { Text("Stack name") }, singleLine = true, supportingText = { if (slug.isNotEmpty()) Text("ID and project: $slug") })
                Text("Device", style = MaterialTheme.typography.labelMedium)
                devices.data?.forEach { d -> FilterChip(agent == d.agentId, { agent = d.agentId }, enabled = d.message.isBlank(), label = { Text(d.name + if (d.message.isNotBlank()) " (unavailable)" else "") }) }
                    ?: Spinner()
            }
        },
        confirmButton = {
            Button(enabled = !busy && slug.isNotEmpty() && agent.isNotEmpty(), onClick = {
                scope.launch {
                    busy = true; error = null
                    try {
                        val r = api.post<CreatedStack>("/api/stacks", buildJsonObject { put("id", slug); put("name", name.trim()); put("projectName", slug); put("agentId", agent) })
                        onDone(r.id.ifBlank { slug })
                    } catch (e: ApiException) { error = e.message }
                    busy = false
                }
            }) { if (busy) Spinner() else Text("Create") }
        },
        dismissButton = { TextButton({ onDone(null) }, enabled = !busy) { Text("Cancel") } },
    )
}
