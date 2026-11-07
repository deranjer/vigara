package org.vigara.mobile.ui.screens

import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Surface
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.painterResource
import org.vigara.mobile.R
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.SetupStatus
import org.vigara.mobile.data.User
import org.vigara.mobile.data.UserEnvelope
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.Spinner

@Composable
fun LoginScreen(api: VigaraApi, initialError: String?, onSignedIn: (User) -> Unit) {
    var server by remember { mutableStateOf(api.serverUrl) }
    var username by remember { mutableStateOf(api.lastUsername) }
    var displayName by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var confirm by remember { mutableStateOf("") }
    var setupRequired by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf(initialError) }
    val scope = rememberCoroutineScope()

    fun submit() {
        scope.launch {
            busy = true
            error = null
            try {
                val normalised = api.setServer(server) ?: throw ApiException(0, "Enter a valid server address, for example vigara.example.com.")
                server = normalised
                val status = api.get<SetupStatus>("/api/setup/status")
                setupRequired = status.setupRequired
                val user = if (status.setupRequired) {
                    if (password != confirm) throw ApiException(0, "The passwords do not match.")
                    api.post<UserEnvelope>("/api/setup/first-user", buildJsonObject {
                        put("username", username.trim()); put("displayName", displayName.trim())
                        put("password", password); put("confirmPassword", confirm)
                    }).user
                } else {
                    api.post<UserEnvelope>("/api/auth/login", buildJsonObject { put("username", username.trim()); put("password", password) }).user
                }
                api.lastUsername = username.trim()
                onSignedIn(user)
            } catch (failure: ApiException) {
                error = failure.message
            } finally {
                busy = false
            }
        }
    }

    Column(
        Modifier.safeDrawingPadding().verticalScroll(rememberScrollState()).padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Surface(color = Color.White, shape = RoundedCornerShape(16.dp), modifier = Modifier.fillMaxWidth()) {
            Image(
                painterResource(R.drawable.vigara_wordmark), contentDescription = "Vigara",
                modifier = Modifier.padding(horizontal = 32.dp, vertical = 20.dp).height(88.dp).fillMaxWidth(),
            )
        }
        Text(
            if (setupRequired) "Create the first administrator for this server." else "Sign in to your self-hosted Vigara server.",
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        error?.let { ErrorBanner(it) }
        OutlinedTextField(
            server, { server = it }, label = { Text("Server address") }, singleLine = true, enabled = !busy,
            placeholder = { Text("https://vigara.example.com") },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), modifier = Modifier.fillMaxWidth(),
        )
        if (server.trim().startsWith("http://")) {
            InfoBanner("This connection is not encrypted. Your password and session travel in clear text; use HTTPS unless this is a trusted private network.")
        }
        OutlinedTextField(username, { username = it }, label = { Text("Username") }, singleLine = true, enabled = !busy, modifier = Modifier.fillMaxWidth())
        if (setupRequired) {
            OutlinedTextField(displayName, { displayName = it }, label = { Text("Display name") }, singleLine = true, enabled = !busy, modifier = Modifier.fillMaxWidth())
        }
        OutlinedTextField(
            password, { password = it }, label = { Text("Password") }, singleLine = true, enabled = !busy,
            visualTransformation = PasswordVisualTransformation(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password), modifier = Modifier.fillMaxWidth(),
        )
        if (setupRequired) {
            OutlinedTextField(
                confirm, { confirm = it }, label = { Text("Confirm password (12+ characters)") }, singleLine = true, enabled = !busy,
                visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password), modifier = Modifier.fillMaxWidth(),
            )
        }
        Button(onClick = ::submit, enabled = !busy && server.isNotBlank() && username.isNotBlank() && password.isNotEmpty(), modifier = Modifier.fillMaxWidth()) {
            if (busy) Spinner() else Text(if (setupRequired) "Create account" else "Sign in")
        }
        if (setupRequired) TextButton(onClick = { setupRequired = false }) { Text("Back to sign in") }
    }
}
