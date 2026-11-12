package org.vigara.mobile.push

import android.Manifest
import android.app.Activity
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.unifiedpush.android.connector.FailedReason
import org.unifiedpush.android.connector.PushService
import org.unifiedpush.android.connector.UnifiedPush
import org.unifiedpush.android.connector.data.PushEndpoint
import org.unifiedpush.android.connector.data.PushMessage
import org.vigara.mobile.MainActivity
import org.vigara.mobile.R
import org.vigara.mobile.VigaraApp
import org.vigara.mobile.data.AppJson
import org.vigara.mobile.data.VigaraApi

/** Where this phone stands with push notifications, as shown on the Notifications screen. */
data class PushStatus(val enabled: Boolean = false, val phoneId: String = "", val distributor: String = "", val error: String = "", val waiting: Boolean = false)

@Serializable private data class PhoneId(val id: String = "")
@Serializable private data class PushKey(val vapidKey: String = "")

/** The decrypted message the server sends (see RenderPush on the server). */
@Serializable private data class AlertPush(
    val kind: String = "", val severity: String = "", val title: String = "", val body: String = "", val incident: String = "", val url: String = "",
)

/**
 * Turns push notifications on and off and keeps the server's record of this phone in step with the
 * UnifiedPush registration. State lives in shared preferences so the background service sees it too.
 */
object Push {
    const val EXTRA_OPEN = "org.vigara.mobile.OPEN"
    private const val PREFS = "vigara_push"
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    val status = MutableStateFlow(PushStatus())
    /** Set when a notification is tapped; the app shell opens that tab and clears it. */
    val openRequest = MutableStateFlow<String?>(null)

    private fun prefs(context: Context) = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    fun load(context: Context) {
        val p = prefs(context)
        status.value = PushStatus(
            enabled = p.getBoolean("enabled", false), phoneId = p.getString("phone", "").orEmpty(),
            distributor = UnifiedPush.getAckDistributor(context).orEmpty(), error = p.getString("error", "").orEmpty(),
        )
    }

    private fun save(context: Context, update: (PushStatus) -> PushStatus) {
        val next = update(status.value)
        status.value = next
        prefs(context).edit().putBoolean("enabled", next.enabled).putString("phone", next.phoneId).putString("error", next.error).apply()
    }

    fun distributors(context: Context): List<String> = UnifiedPush.getDistributors(context).filter { it != context.packageName }

    fun canNotify(context: Context) = Build.VERSION.SDK_INT < 33 ||
        ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    /**
     * Pick the push app (the user's default, or the system chooser) and register with it. The
     * endpoint arrives later in [AlertPushService.onNewEndpoint], which tells the server.
     */
    fun enable(activity: Activity) {
        save(activity) { it.copy(enabled = true, error = "", waiting = true) }
        UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { ok ->
            if (ok) register(activity)
            else save(activity) { it.copy(waiting = false, error = "No push app was chosen.") }
            status.value = status.value.copy(distributor = UnifiedPush.getSavedDistributor(activity).orEmpty())
        }
    }

    /** On start: make sure an enabled phone is still registered and the server has its endpoint. */
    fun refresh(activity: Activity) {
        load(activity)
        if (!status.value.enabled) return
        UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { ok -> if (ok) register(activity) }
    }

    /**
     * Register with the server's VAPID key, so push apps that require one accept the
     * registration and only this server can push to the endpoint. A server without a
     * key (an older version) gets a plain registration.
     */
    private fun register(activity: Activity) {
        val context = activity.applicationContext
        val api = (context as VigaraApp).api
        scope.launch {
            val key = runCatching { api.get<PushKey>("/api/notifications/push-key").vapidKey }.getOrDefault("")
            try {
                UnifiedPush.register(context, vapid = key.ifEmpty { null })
            } catch (e: Exception) {
                save(context) { it.copy(waiting = false, error = "The push app could not register this phone.") }
            }
        }
    }

    /** Stop notifications: unregister from the push app and forget this phone on the server. */
    fun disable(context: Context, api: VigaraApi) {
        val id = status.value.phoneId
        UnifiedPush.unregister(context)
        save(context) { PushStatus() }
        if (id.isNotEmpty()) scope.launch { runCatching { api.delete("/api/notifications/phones/$id") } }
    }

    internal fun onEndpoint(context: Context, api: VigaraApi, endpoint: PushEndpoint) {
        val keys = endpoint.pubKeySet
        if (keys == null) {
            save(context) { it.copy(waiting = false, error = "This push app does not support encrypted messages. Update it, or choose another.") }
            return
        }
        if (!api.hasServer) return
        scope.launch {
            try {
                val name = listOf(Build.MANUFACTURER.replaceFirstChar { it.titlecase() }, Build.MODEL).distinct().joinToString(" ")
                val result = api.put<PhoneId>("/api/notifications/phones", buildJsonObject {
                    put("name", name); put("endpoint", endpoint.url); put("p256dh", keys.pubKey); put("auth", keys.auth)
                })
                save(context) { it.copy(enabled = true, phoneId = result.id, waiting = false, error = "", distributor = UnifiedPush.getAckDistributor(context).orEmpty()) }
            } catch (e: Exception) {
                save(context) { it.copy(waiting = false, error = "The server did not accept this phone: ${e.message}") }
            }
        }
    }

    internal fun onFailed(context: Context, reason: FailedReason) {
        save(context) {
            it.copy(waiting = false, error = when (reason) {
                FailedReason.NETWORK -> "The push app could not reach its server. Try again when online."
                FailedReason.ACTION_REQUIRED -> "The push app needs attention. Open it, then try again."
                FailedReason.VAPID_REQUIRED -> "This push app requires a server key Vigara does not send yet. Choose another push app."
                else -> "The push app could not register this phone."
            })
        }
    }

    internal fun onUnregistered(context: Context, api: VigaraApi) {
        val id = status.value.phoneId
        save(context) { PushStatus(error = "The push app removed this phone's registration.") }
        if (id.isNotEmpty()) scope.launch { runCatching { api.delete("/api/notifications/phones/$id") } }
    }

    // ---- Notifications ----

    private const val CHANNEL_CRITICAL = "alerts_critical"
    private const val CHANNEL_WARNING = "alerts_warning"
    private const val CHANNEL_CLEARED = "alerts_cleared"

    fun createChannels(context: Context) {
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        manager.createNotificationChannels(listOf(
            NotificationChannel(CHANNEL_CRITICAL, "Critical alerts", NotificationManager.IMPORTANCE_HIGH).apply { description = "Alerts marked critical" },
            NotificationChannel(CHANNEL_WARNING, "Warnings", NotificationManager.IMPORTANCE_DEFAULT).apply { description = "Alerts marked warning, and tests" },
            NotificationChannel(CHANNEL_CLEARED, "Cleared alerts", NotificationManager.IMPORTANCE_LOW).apply { description = "When an alert stops firing" },
        ))
    }

    internal fun show(context: Context, message: PushMessage) {
        if (!message.decrypted || !canNotify(context)) return
        val alert = runCatching { AppJson.decodeFromString<AlertPush>(message.content.decodeToString()) }.getOrNull() ?: return
        val channel = when {
            alert.kind == "resolved" -> CHANNEL_CLEARED
            alert.kind == "firing" && alert.severity == "critical" -> CHANNEL_CRITICAL
            else -> CHANNEL_WARNING
        }
        val open = Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP).putExtra(EXTRA_OPEN, "alerts")
        val tap = PendingIntent.getActivity(context, 0, open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val notification = NotificationCompat.Builder(context, channel)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(alert.title)
            .setContentText(alert.body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(alert.body))
            .setContentIntent(tap)
            .setAutoCancel(true)
            .setCategory(NotificationCompat.CATEGORY_STATUS)
            .build()
        // One notification per alert: when it clears, the cleared message replaces the firing one.
        val tag = alert.incident.ifEmpty { alert.kind }
        try { NotificationManagerCompat.from(context).notify(tag, 1, notification) } catch (_: SecurityException) { }
    }
}

/** Receives endpoints and alerts from the UnifiedPush distributor. */
class AlertPushService : PushService() {
    private val api get() = (application as VigaraApp).api

    override fun onNewEndpoint(endpoint: PushEndpoint, instance: String) = Push.onEndpoint(this, api, endpoint)
    override fun onMessage(message: PushMessage, instance: String) = Push.show(this, message)
    override fun onRegistrationFailed(reason: FailedReason, instance: String) = Push.onFailed(this, reason)
    override fun onUnregistered(instance: String) = Push.onUnregistered(this, api)
}
