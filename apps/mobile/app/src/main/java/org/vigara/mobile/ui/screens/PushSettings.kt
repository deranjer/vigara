package org.vigara.mobile.ui.screens

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import kotlinx.serialization.Serializable
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.push.Push
import org.vigara.mobile.ui.common.AppScaffold
import org.vigara.mobile.ui.common.ErrorBanner
import org.vigara.mobile.ui.common.InfoBanner
import org.vigara.mobile.ui.common.KeyValue
import org.vigara.mobile.ui.common.Palette
import org.vigara.mobile.ui.common.SectionTitle
import org.vigara.mobile.ui.common.StatusText

@Serializable private data class TestResult(val ok: Boolean = false, val error: String = "")

private val distributorNames = mapOf("io.heckel.ntfy" to "ntfy", "org.unifiedpush.distributor.nextpush" to "NextPush", "org.unifiedpush.distributor.sunup" to "Sunup")
private val testErrors = mapOf("gone" to "the push app no longer has this phone registered", "request_failed" to "the push server could not be reached", "rejected" to "the push server refused the message")

/** Alerts on this phone, delivered through the user's UnifiedPush app (for example ntfy). */
@Composable
fun PushSettingsScreen(api: VigaraApi, onBack: () -> Unit) {
    val context = LocalContext.current
    val activity = context as Activity
    val status by Push.status.collectAsState()
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    var checks by remember { mutableIntStateOf(0) }
    var testing by remember { mutableStateOf(false) }
    // Re-read installed push apps and the permission whenever the screen comes back into view.
    val distributors = remember(checks) { Push.distributors(context) }
    val allowed = remember(checks) { Push.canNotify(context) }
    LaunchedEffect(Unit) { Push.load(context) }
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        checks++
        if (granted) Push.enable(activity)
    }
    androidx.lifecycle.compose.LifecycleResumeEffect(Unit) { checks++; onPauseOrDispose { } }

    AppScaffold("Notifications", onBack = onBack, snackbar = snackbar) { padding ->
        Column(Modifier.padding(padding).padding(16.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text("Alerts from this server can arrive on this phone as notifications, even when the app is closed.", style = MaterialTheme.typography.bodyMedium)
            val on = status.enabled && status.phoneId.isNotEmpty()
            StatusText(
                when { on -> "On"; status.waiting -> "Waiting for the push app…"; else -> "Off" },
                when { on -> Palette.good; status.waiting -> Palette.warn; else -> Palette.idle },
            )
            if (status.error.isNotEmpty()) ErrorBanner(status.error)
            if (on && !allowed) ErrorBanner("Android is blocking notifications from Vigara. Allow them in the system settings.")

            if (distributors.isEmpty() && !on) {
                InfoBanner("Notifications are delivered by a UnifiedPush app, which keeps one connection open for all your apps. Install ntfy, open it once, then come back here.")
                Button({ context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse("https://f-droid.org/packages/io.heckel.ntfy/"))) }) { Text("Get ntfy from F-Droid") }
                TextButton({ context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse("https://unifiedpush.org/users/distributors/"))) }) { Text("Other push apps") }
            } else if (!on) {
                Button({
                    if (Build.VERSION.SDK_INT >= 33 && !allowed) permission.launch(Manifest.permission.POST_NOTIFICATIONS) else Push.enable(activity)
                }, enabled = !status.waiting) { Text("Turn on notifications") }
            }

            if (on) {
                SectionTitle("This phone")
                KeyValue("Push app", distributorNames[status.distributor] ?: status.distributor.ifEmpty { "—" })
                KeyValue("Server", api.serverUrl)
                Button({
                    scope.launch {
                        testing = true
                        try {
                            val r = api.post<TestResult>("/api/notifications/phones/${status.phoneId}/test")
                            snackbar.showSnackbar(if (r.ok) "Test sent." else "Test failed: ${testErrors[r.error] ?: r.error.replace('_', ' ')}.")
                        } catch (e: ApiException) {
                            snackbar.showSnackbar(e.message ?: "The test could not be sent.")
                        } finally { testing = false }
                    }
                }, enabled = !testing) { Text("Send a test notification") }
                OutlinedButton({ Push.disable(context, api) }) { Text("Turn off") }
            }

            SectionTitle("How it works")
            Text(
                "Vigara encrypts each alert for this phone before sending it, so the push app and its server only pass it on. " +
                    "Which alerts are sent, and quiet hours, are set by an admin under Settings › Notifications on the web. " +
                    "Muted alerts are never sent.",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            if (on) TextButton({
                val intent = Intent(android.provider.Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(android.provider.Settings.EXTRA_APP_PACKAGE, context.packageName)
                context.startActivity(intent)
            }) { Text("Notification sounds and categories") }
        }
    }
}
