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
        WorkManager.getInstance(context).enqueueUniqueWork("vigara-android-selected-file-backup-now", androidx.work.ExistingWorkPolicy.KEEP, OneTimeWorkRequestBuilder<BackupWorker>().setConstraints(constraints).addTag("vigara-backup").build())
    }
}

class TelemetryWorker(context: Context, params: WorkerParameters) : CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        return try {
            val prefs = CompanionPrefs(applicationContext)
            val enrollment = prefs.enrollmentSnapshot()
            if (enrollment.credential.isEmpty() || enrollment.agentId.isEmpty() || enrollment.generation.isEmpty()) return Result.failure()
            val body = enrollment.pendingTelemetryBody.ifBlank {
                val uptime = SystemClock.elapsedRealtime() / 1000
                val snapshot = JSONObject().apply {
                    put("protocolVersion", "v1"); put("id", UUID.randomUUID().toString()); put("agentId", enrollment.agentId); put("observedAt", Instant.now().toString())
                    put("host", JSONObject().apply { put("os", "android"); put("osVersion", Build.VERSION.RELEASE.take(256)); put("architecture", Build.SUPPORTED_ABIS.firstOrNull() ?: "unknown"); put("agentVersion", "0.1.0"); put("uptimeSeconds", uptime) })
                    put("metrics", JSONArray().put(JSONObject().put("name", "uptime_seconds").put("value", uptime.toDouble()).put("unit", "s")))
                    put("capabilities", JSONArray().put("system.uptime"))
                }
                JSONObject().put("protocolVersion", "v1").put("agentId", enrollment.agentId).put("snapshots", JSONArray().put(snapshot)).toString().also { if (!prefs.storeTelemetry(enrollment.generation, it)) return Result.success() }
            }
            if (prefs.generation != enrollment.generation) return Result.success()
            val code = request(enrollment.server, "/api/agent/telemetry", enrollment.credential, body.toByteArray(), "application/json")
            if (code in 200..299) { prefs.clearTelemetry(enrollment.generation, body); Result.success() }
            else if (code == 429 || code >= 500) Result.retry() else Result.failure()
        } catch (_: IOException) { Result.retry() } catch (_: Exception) { Result.failure() }
    }
}

class BackupWorker(context: Context, params: WorkerParameters) : CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        val prefs = CompanionPrefs(applicationContext)
        val db = SelectedFiles(applicationContext)
        val enrollment = prefs.enrollmentSnapshot()
        val generation = enrollment.generation
        if (enrollment.credential.isEmpty() || enrollment.agentId.isEmpty() || !isHttpsServer(enrollment.server)) return Result.failure()
        if (generation.isEmpty()) return Result.failure()
        var retry = false
        for (pending in db.pending(generation)) {
            if (isStopped || prefs.generation != generation) break
            val id = pending.selected.id
            val uri = Uri.parse(pending.selected.uri)
            try {
                db.state(id, generation, "Checking selected file")
                val current = db.metadata(applicationContext.contentResolver, uri)
                if (current == null) {
                    db.state(id, generation, "File unavailable; select it again")
                    continue
                }
                if (!SelectedFiles.supportedSize(current.second)) {
                    db.state(id, generation, "Unsupported file size; select a file between 1 byte and 512 MB")
                    continue
                }
                val selected = SelectedFiles.refreshMetadata(pending.selected, current.first, current.second)
                if (selected == null) {
                    db.state(id, generation, "Unsupported file name; select it again")
                    continue
                }
                if (isStopped || prefs.generation != generation) break
                val hash = digest(uri, selected.size)
                if (hash == pending.lastHash) {
                    db.state(id, generation, "Backed up; no changes")
                    continue
                }
                val backupId = if (pending.pendingHash == hash && pending.pendingBackupId.isNotBlank()) pending.pendingBackupId else UUID.randomUUID().toString().also { db.pendingVersion(id, generation, hash, it) }
                if (isStopped || prefs.generation != generation) break
                db.state(id, generation, "Uploading selected file")
                val code = upload(enrollment.server, enrollment.credential, uri, selected, backupId, hash, generation, prefs)
                when {
                    code == 201 || code == 204 -> db.uploaded(id, generation, hash)
                    code == 401 || code == 403 -> db.state(id, generation, "Agent credential or upload permission needs attention")
                    code == 429 || code >= 500 -> { db.state(id, generation, "Waiting for server"); retry = true }
                    code == 507 -> db.state(id, generation, "Server storage quota is full")
                    code == 503 -> db.state(id, generation, "Server file backup storage is disabled")
                    code == 413 -> db.state(id, generation, "File exceeds the server's size limit")
                    code == 409 -> db.state(id, generation, "File version conflicts with an earlier upload; remove and select it again")
                    else -> db.state(id, generation, "Server rejected this file ($code)")
                }
            } catch (_: SecurityException) {
                db.state(id, generation, "Access was revoked; select the file again")
            } catch (_: IOException) {
                if (prefs.generation != generation) break
                db.state(id, generation, "Waiting for connection")
                retry = true
            } catch (_: Exception) {
                db.state(id, generation, "Could not read this file")
            }
        }
        db.close()
        return if (retry) Result.retry() else Result.success()
    }

    private fun digest(uri: Uri, expectedSize: Long): String {
        val input = applicationContext.contentResolver.openInputStream(uri) ?: throw IOException("Selected file cannot be opened")
        return SelectedFiles.digestSized(input, expectedSize)
    }

    private fun upload(server: String, credential: String, uri: Uri, selected: SelectedFile, backupId: String, hash: String, generation: String, prefs: CompanionPrefs): Int {
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
                val streamedHash = MessageDigest.getInstance("SHA-256")
                while (true) {
                    if (isStopped || prefs.generation != generation) throw IOException("Upload cancelled")
                    val count = source.read(buffer); if (count < 0) break
                    total += count
                    if (total > selected.size) throw IOException("Selected file changed during upload")
                    streamedHash.update(buffer, 0, count)
                    target.write(buffer, 0, count)
                }
                if (total != selected.size || streamedHash.digest().joinToString("") { "%02x".format(it) } != hash) throw IOException("Selected file changed during upload")
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
