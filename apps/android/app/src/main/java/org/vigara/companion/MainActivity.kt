package org.vigara.companion

import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.net.Uri
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.lifecycle.Observer
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL

class MainActivity : ComponentActivity() {
    private lateinit var prefs: CompanionPrefs
    private lateinit var files: SelectedFiles
    private lateinit var server: EditText
    private lateinit var deviceName: EditText
    private lateinit var enrollmentToken: EditText
    private lateinit var status: TextView
    private lateinit var selectedList: LinearLayout

    private val picker = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode == Activity.RESULT_OK) {
            val data = result.data
            if (data != null) {
                val selected = mutableListOf<Uri>()
                data.clipData?.let { clip -> for (index in 0 until clip.itemCount) selected.add(clip.getItemAt(index).uri) }
                if (selected.isEmpty()) data.data?.let(selected::add)
                var count = 0
                selected.forEach { if (files.add(contentResolver, it, data.flags)) count++ }
                status.text = if (count > 0) "$count selected file${if (count == 1) "" else "s"} added" else "No files were added. Choose files with readable sizes and persistent access."
                refreshFiles()
            }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = CompanionPrefs(this)
        files = SelectedFiles(this)
        buildScreen()
        server.setText(prefs.server)
        if (prefs.agentId.isNotBlank()) {
            status.text = "Connected • ${prefs.agentId.take(8)}…"
            CompanionSchedule.schedule(this)
        }
        androidx.work.WorkManager.getInstance(this).getWorkInfosByTagLiveData("vigara-backup").observe(this, Observer { works ->
            refreshFiles()
            if (works.any { it.state == androidx.work.WorkInfo.State.SUCCEEDED || it.state == androidx.work.WorkInfo.State.FAILED }) {
                if (works.any { it.state == androidx.work.WorkInfo.State.SUCCEEDED }) status.text = "Backup check finished. Review each selected file's status below."
            }
        })
        refreshFiles()
    }

    private fun buildScreen() {
        window.statusBarColor = Color.rgb(246, 248, 246)
        window.navigationBarColor = Color.rgb(246, 248, 246)
        window.decorView.systemUiVisibility = android.view.View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR or android.view.View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
        val page = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(24, 28, 24, 28); setBackgroundColor(Color.rgb(246, 248, 246)) }
        val title = TextView(this).apply { text = "Vigara"; textSize = 28f; setTextColor(Color.rgb(32, 55, 44)); gravity = Gravity.CENTER_VERTICAL }
        val subtitle = TextView(this).apply { text = "Android companion"; textSize = 15f; setTextColor(Color.rgb(91, 106, 97)); setPadding(0, 0, 0, 18) }
        page.addView(title); page.addView(subtitle)
        page.addView(label("Server HTTPS address"))
        server = field("https://vigara.local")
        page.addView(server)
        page.addView(label("Device name"))
        deviceName = field("Android phone")
        page.addView(deviceName)
        page.addView(label("One-use enrollment token"))
        enrollmentToken = field("Paste token from Vigara", true)
        page.addView(enrollmentToken)
        page.addView(button("Connect securely") { enroll() })
        status = TextView(this).apply { textSize = 14f; setTextColor(Color.rgb(55, 76, 63)); setPadding(0, 16, 0, 18) }
        page.addView(status)
        page.addView(label("Files you selected"))
        page.addView(TextView(this).apply { text = "Only selected files are readable. Access can be revoked in Android settings."; textSize = 13f; setTextColor(Color.rgb(91, 106, 97)); setPadding(0, 4, 0, 10) })
        page.addView(button("Choose files") { openPicker() })
        page.addView(button("Back up selected files now") {
            if (prefs.credential.isEmpty()) status.text = "Connect this device before starting a backup"
            else { CompanionSchedule.backupNow(this); status.text = "Backup queued. Android may wait for a suitable network and battery level." }
        })
        page.addView(TextView(this).apply { text = "Automatic backup runs about once a day on unmetered Wi-Fi when battery is not low. Android controls the exact timing."; textSize = 12f; setTextColor(Color.rgb(91, 106, 97)); setPadding(0, 8, 0, 12) })
        selectedList = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        page.addView(selectedList)
        val scroll = ScrollView(this)
        scroll.addView(page)
        setContentView(scroll)
    }

    private fun enroll() {
        val base = server.text.toString().trim().trimEnd('/')
        val name = deviceName.text.toString().trim()
        val token = enrollmentToken.text.toString().trim()
        if (!isHttpsServer(base) || name.isBlank() || token.length !in 32..128) {
            status.text = "Enter an HTTPS server address, a device name, and a valid one-use token"
            return
        }
        status.text = "Connecting…"
        enrollmentToken.text.clear()
        Thread {
            try {
                val capability = JSONObject().put("protocolVersion", "v1").put("platform", "android")
                    .put("architecture", android.os.Build.SUPPORTED_ABIS.firstOrNull() ?: "unknown")
                    .put("agentVersion", "0.1.0").put("capabilities", JSONArray().put("android.files.upload"))
                val body = JSONObject().put("token", token).put("displayName", name).put("capability", capability).toString().toByteArray()
                val connection = (URL(base + "/api/agent/enroll").openConnection() as HttpURLConnection).apply {
                    requestMethod = "POST"; connectTimeout = 20_000; readTimeout = 30_000; instanceFollowRedirects = false
                    doOutput = true; setFixedLengthStreamingMode(body.size); setRequestProperty("Content-Type", "application/json")
                }
                val response = try { connection.outputStream.use { it.write(body) }; connection.responseCode to if (connection.responseCode in 200..299) connection.inputStream.bufferedReader().use { it.readText() } else "" } finally { connection.disconnect() }
                runOnUiThread {
                    if (response.first in 200..299) {
                        val result = JSONObject(response.second)
                        prefs.server = base; prefs.agentId = result.getString("agentId"); prefs.credential = result.getString("credential")
                        CompanionSchedule.schedule(this)
                        status.text = "Connected • ${prefs.agentId.take(8)}…"
                    } else status.text = "Enrollment was rejected (${response.first}). Check the token scope and expiry."
                }
            } catch (_: Exception) {
                runOnUiThread { status.text = "Could not connect. Check the HTTPS address, certificate, and network." }
            }
        }.start()
    }

    private fun openPicker() {
        val intent = Intent(Intent.ACTION_OPEN_DOCUMENT).apply {
            addCategory(Intent.CATEGORY_OPENABLE); type = "*/*"; putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true)
        }
        picker.launch(intent)
    }

    private fun refreshFiles() {
        if (!::selectedList.isInitialized) return
        selectedList.removeAllViews()
        files.all().forEach { file ->
            val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER_VERTICAL; setPadding(0, 8, 0, 8) }
            val details = TextView(this).apply { text = "${file.name}\n${formatBytes(file.size)} • ${file.state}"; textSize = 14f; setTextColor(Color.rgb(38, 57, 46)); layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f) }
            val remove = Button(this).apply { text = "Remove"; setOnClickListener {
                try { contentResolver.releasePersistableUriPermission(Uri.parse(file.uri), Intent.FLAG_GRANT_READ_URI_PERMISSION) } catch (_: SecurityException) { }
                files.remove(file.id); refreshFiles()
            } }
            row.addView(details); row.addView(remove); selectedList.addView(row)
        }
    }

    private fun label(text: String) = TextView(this).apply { this.text = text; textSize = 13f; setTextColor(Color.rgb(59, 78, 65)); setPadding(0, 9, 0, 4) }
    private fun field(hintText: String, secret: Boolean = false) = EditText(this).apply {
        hint = hintText; textSize = 15f; setSingleLine(true)
        if (secret) inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD else inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
    }
    private fun button(text: String, action: () -> Unit) = Button(this).apply { this.text = text; isAllCaps = false; setOnClickListener { action() } }
    private fun formatBytes(value: Long): String = when {
        value >= 1024L * 1024 * 1024 -> "%.1f GB".format(value.toDouble() / (1024.0 * 1024 * 1024))
        value >= 1024L * 1024 -> "%.1f MB".format(value.toDouble() / (1024.0 * 1024))
        value >= 1024 -> "%.1f KB".format(value.toDouble() / 1024)
        else -> "$value bytes"
    }
}
