package org.vigara.mobile.ui.screens

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
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.PrimaryTabRow
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
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
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.vigara.mobile.BuildConfig
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.Incident
import org.vigara.mobile.data.ManagedUser
import org.vigara.mobile.data.ScheduledTask
import org.vigara.mobile.data.User
import org.vigara.mobile.data.UserEnvelope
import org.vigara.mobile.data.VersionInfo
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.EmptyState
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.ItemCard
import org.vigara.mobile.ui.common.KeyValue
import org.vigara.mobile.ui.common.LoadedContent
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.Spinner
import org.vigara.mobile.ui.common.StatusChip
import org.vigara.mobile.ui.common.formatTime
import org.vigara.mobile.ui.common.rememberLoad
import org.vigara.mobile.ui.common.relativeTime

@Composable
fun MoreScreen(user: User, onNavigate: (String) -> Unit) {
    AppScaffold("More") { padding ->
        Column(Modifier.padding(padding).verticalScroll(rememberScrollState())) {
            SectionTitle("Fleet", Modifier.padding(horizontal = 16.dp))
            MoreRow("Devices", "Every device, its health and metrics") { onNavigate("devices") }
            MoreRow("Image updates", "Newer images for running containers") { onNavigate("updates") }
            if (user.isAdmin) MoreRow("Add device", "Create an agent installer") { onNavigate("add-device") }
            SectionTitle("Docker", Modifier.padding(horizontal = 16.dp))
            MoreRow("Images", "Pull, inspect and prune images") { onNavigate("resources/image") }
            MoreRow("Networks", "Docker networks per host") { onNavigate("resources/network") }
            MoreRow("Volumes", "Docker volumes per host") { onNavigate("resources/volume") }
            SectionTitle("Integrations", Modifier.padding(horizontal = 16.dp))
            MoreRow("Claude and ChatGPT (MCP)", "Connection URL and connected applications") { onNavigate("mcp") }
            if (user.isAdmin) MoreRow("Git settings", "Repository for stack storage") { onNavigate("git-settings") }
            SectionTitle("Account", Modifier.padding(horizontal = 16.dp))
            MoreRow(user.displayName.ifBlank { user.username }, "${user.username} · ${user.role} — password and sign out") { onNavigate("account") }
            if (user.isAdmin) MoreRow("Administration", "Users and scheduled tasks") { onNavigate("admin") }
        }
    }
}

@Composable
private fun MoreRow(title: String, subtitle: String, onClick: () -> Unit) {
    ListItem(
        headlineContent = { Text(title) },
        supportingContent = { Text(subtitle) },
        modifier = Modifier.clickable(onClick = onClick),
    )
    HorizontalDivider()
}

@Composable
fun AccountScreen(api: VigaraApi, user: User, onBack: () -> Unit, onUserChanged: (User) -> Unit, onSignedOut: () -> Unit) {
    val scope = rememberCoroutineScope()
    var current by remember { mutableStateOf("") }
    var next by remember { mutableStateOf("") }
    var confirm by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var done by remember { mutableStateOf(false) }
    val version = rememberLoad { runCatching { api.get<VersionInfo>("/api/version").version }.getOrDefault("") }
    val pw = PasswordVisualTransformation()
    val kb = KeyboardOptions(keyboardType = KeyboardType.Password)

    AppScaffold("Account", onBack = onBack) { padding ->
        Column(Modifier.padding(padding).padding(16.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            KeyValue("Name", user.displayName)
            KeyValue("Username", user.username)
            KeyValue("Role", user.role)
            KeyValue("Server", api.serverUrl)
            KeyValue("Server version", version.data.orEmpty())
            KeyValue("App version", BuildConfig.VERSION_NAME)

            SectionTitle("Change password")
            if (done) InfoBanner("Password changed.")
            error?.let { ErrorBanner(it) }
            OutlinedTextField(current, { current = it; done = false }, label = { Text("Current password") }, singleLine = true, visualTransformation = pw, keyboardOptions = kb, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(next, { next = it; done = false }, label = { Text("New password (12+ characters)") }, singleLine = true, visualTransformation = pw, keyboardOptions = kb, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(confirm, { confirm = it; done = false }, label = { Text("Confirm new password") }, singleLine = true, visualTransformation = pw, keyboardOptions = kb, modifier = Modifier.fillMaxWidth())
            Button(enabled = !busy && current.isNotEmpty() && next.length >= 12 && next == confirm, onClick = {
                scope.launch {
                    busy = true; error = null
                    try {
                        val updated = api.post<UserEnvelope>("/api/auth/password", buildJsonObject { put("currentPassword", current); put("newPassword", next); put("confirmPassword", confirm) })
                        onUserChanged(updated.user.takeIf { it.id.isNotEmpty() } ?: user)
                        current = ""; next = ""; confirm = ""; done = true
                    } catch (e: ApiException) { error = e.message }
                    busy = false
                }
            }) { if (busy) Spinner() else Text("Change password") }

            SectionTitle("Session")
            OutlinedButton({
                scope.launch {
                    runCatching { api.postUnit("/api/auth/logout") }
                    api.forgetSession()
                    onSignedOut()
                }
            }) { Text("Sign out") }
        }
    }
}

@Composable
fun AdminScreen(api: VigaraApi, user: User, onBack: () -> Unit) {
    var tab by remember { mutableStateOf(0) }
    val snackbar = remember { SnackbarHostState() }
    AppScaffold("Administration", onBack = onBack, snackbar = snackbar) { padding ->
        Column(Modifier.padding(padding)) {
            if (!user.isAdmin) { EmptyState("Administrator access required."); return@Column }
            PrimaryTabRow(tab) { listOf("Scheduled tasks", "Users").forEachIndexed { i, t -> Tab(tab == i, { tab = i }, text = { Text(t) }) } }
            if (tab == 0) TasksTab(api, snackbar) else UsersTab(api, user, snackbar)
        }
    }
}

@Composable
private fun TasksTab(api: VigaraApi, snackbar: SnackbarHostState) {
    val tasks = rememberLoad(pollMs = 15_000) { api.get<List<ScheduledTask>>("/api/admin/scheduled-tasks") }
    val scope = rememberCoroutineScope()
    LoadedContent(tasks) { list ->
        LazyColumn(contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            items(list, key = { it.id }) { task ->
                ItemCard {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(task.name, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                        Switch(task.enabled, { enabled ->
                            scope.launch {
                                try { api.patchUnit("/api/admin/scheduled-tasks/${task.id}", buildJsonObject { put("enabled", enabled); put("settings", JsonObject(emptyMap())) }) }
                                catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "Could not update the task.") }
                                tasks.refresh()
                            }
                        }, enabled = task.available)
                    }
                    Text(task.description, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    KeyValue("Status", task.status)
                    KeyValue("Last run", formatTime(task.lastFinishedAt ?: task.lastStartedAt, "never"))
                    KeyValue("Next run", formatTime(task.nextRunAt))
                    if (task.lastError.isNotBlank()) Text(task.lastError, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                    TextButton({
                        scope.launch {
                            try { api.postUnit("/api/admin/scheduled-tasks/${task.id}/run", buildJsonObject { }); snackbar.showSnackbar("Task started.") }
                            catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "Could not start the task.") }
                            tasks.refresh()
                        }
                    }, enabled = task.available) { Text("Run now") }
                }
            }
        }
    }
}

@Composable
private fun UsersTab(api: VigaraApi, me: User, snackbar: SnackbarHostState) {
    val users = rememberLoad { api.get<List<ManagedUser>>("/api/users") }
    val scope = rememberCoroutineScope()
    var creating by remember { mutableStateOf(false) }
    LoadedContent(users) { list ->
        LazyColumn(contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            item { Button({ creating = true }) { Text("Add user") } }
            items(list, key = { it.id }) { u ->
                ItemCard {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text(u.displayName.ifBlank { u.username }, style = MaterialTheme.typography.titleSmall)
                            Text("${u.username} · added ${relativeTime(u.createdAt)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                        if (u.id == me.id) StatusChip("you", Palette.info)
                    }
                    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        listOf("viewer", "operator", "admin").forEach { role ->
                            FilterChip(u.role == role, {
                                if (u.id != me.id && u.role != role) scope.launch {
                                    try { api.patchUnit("/api/users/${u.id}", buildJsonObject { put("role", role) }) }
                                    catch (e: ApiException) { snackbar.showSnackbar(e.message ?: "Could not change the role.") }
                                    users.refresh()
                                }
                            }, label = { Text(role) }, enabled = u.id != me.id)
                        }
                    }
                }
            }
        }
    }
    if (creating) CreateUserDialog(api, onDone = { creating = false; users.refresh() })
}

@Composable
private fun CreateUserDialog(api: VigaraApi, onDone: () -> Unit) {
    var username by remember { mutableStateOf("") }
    var display by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var role by remember { mutableStateOf("viewer") }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    AlertDialog(
        onDismissRequest = { if (!busy) onDone() },
        title = { Text("Add user") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                error?.let { ErrorBanner(it) }
                OutlinedTextField(username, { username = it }, label = { Text("Username") }, singleLine = true)
                OutlinedTextField(display, { display = it }, label = { Text("Display name") }, singleLine = true)
                OutlinedTextField(password, { password = it }, label = { Text("Password (12+ characters)") }, singleLine = true, visualTransformation = PasswordVisualTransformation())
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    listOf("viewer", "operator", "admin").forEach { r -> FilterChip(role == r, { role = r }, label = { Text(r) }) }
                }
            }
        },
        confirmButton = {
            Button(enabled = !busy && username.length >= 3 && password.length >= 12, onClick = {
                scope.launch {
                    busy = true; error = null
                    try {
                        api.postUnit("/api/users", buildJsonObject { put("username", username.trim()); put("displayName", display.trim()); put("password", password); put("role", role) })
                        onDone()
                    } catch (e: ApiException) { error = e.message }
                    busy = false
                }
            }) { if (busy) Spinner() else Text("Create") }
        },
        dismissButton = { TextButton(onDone, enabled = !busy) { Text("Cancel") } },
    )
}
