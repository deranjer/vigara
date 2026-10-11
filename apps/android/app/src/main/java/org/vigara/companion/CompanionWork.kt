package org.vigara.companion

import android.content.Context
import android.net.Uri
import android.os.Build
import android.os.SystemClock
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import java.time.Instant
import java.util.Base64
import java.util.UUID
import java.util.concurrent.TimeUnit

object CompanionSchedule {
    fun schedule(context: Context) {
        val connected = Constraints.Builder().setRequiredNetworkType(NetworkType.UNMETERED).setRequiresBatteryNotLow(true).build()
        val telemetry = PeriodicWorkRequestBuilder<TelemetryWorker>(6, TimeUnit.HOURS).setConstraints(connected).addTag("vigara-telemetry").build()
        val backups = PeriodicWorkRequestBuilder<BackupWorker>(24, TimeUnit.HOURS).setConstraints(connected).addTag("vigara-backup").build()
        WorkManager.getInstance(context).enqueueUniquePeriodicWork("vigara-android-telemetry", ExistingPeriodicWorkPolicy.KEEP, telemetry)
        WorkManager.getInstance(context).enqueueUniquePeriodicWork("vigara-android-selected-file-backup", ExistingPeriodicWorkPolicy.KEEP, backups)
    }

    fun backupNow(context: Context) {
        val constraints = Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).setRequiresBatteryNotLow(true).build()
        WorkManager.getInstance(context).enqueue(OneTimeWorkRequestBuilder<BackupWorker>().setConstraints(constraints).addTag("vigara-backup").build())
    }
}

class TelemetryWorker(context: Context, params: WorkerParameters) : CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        return try {
            val prefs = CompanionPrefs(applicationContext)
            val credential = prefs.credential
            if (credential.isEmpty() || prefs.agentId.isEmpty()) return Result.failure()
            val body = prefs.pendingTelemetryBody.ifBlank {
                val uptime = SystemClock.elapsedRealtime() / 1000
                val snapshot = JSONObject().apply {
                    put("protocolVersion", "v1"); put("id", UUID.randomUUID().toString()); put("agentId", prefs.agentId); put("observedAt", Instant.now().toString())
                    put("host", JSONObject().apply { put("os", "android"); put("osVersion", Build.VERSION.RELEASE.take(256)); put("architecture", Build.SUPPORTED_ABIS.firstOrNull() ?: "unknown"); put("agentVersion", "0.1.0"); put("uptimeSeconds", uptime) })
                    put("metrics", JSONArray().put(JSONObject().put("name", "uptime_seconds").put("value", uptime.toDouble()).put("unit", "s")))
                    put("capabilities", JSONArray().put("system.uptime"))
                }
                JSONObject().put("protocolVersion", "v1").put("agentId", prefs.agentId).put("snapshots", JSONArray().put(snapshot)).toString().also { prefs.pendingTelemetryBody = it }
            }.toByteArray()
            val code = request(prefs.server, "/api/agent/telemetry", credential, body, "application/json")
            if (code in 200..299) { prefs.pendingTelemetryBody = ""; Result.success() }
            else if (code == 429 || code >= 500) Result.retry() else Result.failure()
        } catch (_: IOException) { Result.retry() } catch (_: Exception) { Result.failure() }
    }
}

class BackupWorker(context: Context, params: WorkerParameters) : CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        val prefs = CompanionPrefs(applicationContext)
        val db = SelectedFiles(applicationContext)
        val credential = prefs.credential
        if (credential.isEmpty() || prefs.agentId.isEmpty() || !isHttpsServer(prefs.server)) return Result.failure()
        var retry = false
        for (pending in db.pending()) {
            val id = pending.selected.id
            val uri = Uri.parse(pending.selected.uri)
            try {
                db.state(id, "Checking selected file")
                val current = db.metadata(applicationContext.contentResolver, uri)
                if (current == null || current.second != pending.selected.size) {
                    db.state(id, "File unavailable; select it again")
                    continue
                }
                val hash = digest(uri)
                if (hash == pending.lastHash) {
                    db.state(id, "Backed up; no changes")
                    continue
                }
                val backupId = if (pending.pendingHash == hash && pending.pendingBackupId.isNotBlank()) pending.pendingBackupId else UUID.randomUUID().toString().also { db.pendingVersion(id, hash, it) }
                db.state(id, "Uploading selected file")
                val code = upload(prefs.server, credential, uri, pending.selected, backupId, hash)
                when {
                    code == 201 || code == 204 -> db.uploaded(id, hash)
                    code == 401 || code == 403 -> db.state(id, "Agent credential or upload permission needs attention")
                    code == 429 || code >= 500 -> { db.state(id, "Waiting for server"); retry = true }
                    code == 507 -> db.state(id, "Server storage quota is full")
                    code == 503 -> db.state(id, "Server file backup storage is disabled")
                    code == 413 -> db.state(id, "File exceeds the server's size limit")
                    code == 409 -> db.state(id, "File version conflicts with an earlier upload; remove and select it again")
                    else -> db.state(id, "Server rejected this file ($code)")
                }
            } catch (_: SecurityException) {
                db.state(id, "Access was revoked; select the file again")
            } catch (_: IOException) {
                db.state(id, "Waiting for connection")
                retry = true
            } catch (_: Exception) {
                db.state(id, "Could not read this file")
            }
        }
        db.close()
        return if (retry) Result.retry() else Result.success()
    }

    private fun digest(uri: Uri): String {
        val digest = MessageDigest.getInstance("SHA-256")
        applicationContext.contentResolver.openInputStream(uri)?.use { input ->
            val buffer = ByteArray(64 * 1024)
            while (true) { val count = input.read(buffer); if (count < 0) break; digest.update(buffer, 0, count) }
        } ?: throw IOException("Selected file cannot be opened")
        return digest.digest().joinToString("") { "%02x".format(it) }
    }

    private fun upload(server: String, credential: String, uri: Uri, selected: SelectedFile, backupId: String, hash: String): Int {
        val url = URL(server.trimEnd('/') + "/api/agent/android/files")
        val connection = (url.openConnection() as HttpURLConnection).apply {
            requestMethod = "PUT"; connectTimeout = 20_000; readTimeout = 60_000; instanceFollowRedirects = false
            doOutput = true; setFixedLengthStreamingMode(selected.size)
            setRequestProperty("Authorization", "Bearer $credential")
            setRequestProperty("X-Vigara-Backup-ID", backupId)
            setRequestProperty("X-Vigara-File-ID", selected.id)
            setRequestProperty("X-Vigara-File-Name", Base64.getUrlEncoder().withoutPadding().encodeToString(selected.name.toByteArray(Charsets.UTF_8)))
            setRequestProperty("X-Vigara-Content-SHA256", hash)
            setRequestProperty("Content-Type", "application/octet-stream")
        }
        try {
            val input = applicationContext.contentResolver.openInputStream(uri) ?: throw IOException("Selected file cannot be opened")
            input.use { source -> connection.outputStream.use { target ->
                val buffer = ByteArray(64 * 1024)
                var total = 0L
                while (true) {
                    if (isStopped) throw IOException("Upload cancelled")
                    val count = source.read(buffer); if (count < 0) break
                    total += count
                    if (total > selected.size) throw IOException("Selected file changed during upload")
                    target.write(buffer, 0, count)
                }
                if (total != selected.size) throw IOException("Selected file changed during upload")
            } }
            return connection.responseCode
        } finally { connection.disconnect() }
    }
}

internal fun isHttpsServer(value: String): Boolean = try {
    val url = URL(value)
    url.protocol == "https" && !url.host.isNullOrBlank() && url.userInfo == null && url.query == null && url.ref == null
} catch (_: Exception) { false }

private fun request(server: String, path: String, credential: String, body: ByteArray, contentType: String): Int {
    if (!isHttpsServer(server)) throw IOException("HTTPS server URL required")
    val connection = URL(server.trimEnd('/') + path).openConnection() as HttpURLConnection
    connection.requestMethod = "POST"; connection.connectTimeout = 20_000; connection.readTimeout = 30_000; connection.instanceFollowRedirects = false
    connection.doOutput = true; connection.setFixedLengthStreamingMode(body.size)
    connection.setRequestProperty("Authorization", "Bearer $credential"); connection.setRequestProperty("Content-Type", contentType)
    return try { connection.outputStream.use { it.write(body) }; connection.responseCode } finally { connection.disconnect() }
}
