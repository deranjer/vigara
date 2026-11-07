package org.vigara.mobile.ui.common

import androidx.compose.ui.graphics.Color
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import java.util.Locale

fun formatBytes(value: Long, digits: Int = 1): String = formatBytes(value.toDouble(), digits)

fun formatBytes(value: Double, digits: Int = 1): String {
    if (!value.isFinite() || value < 0) return "–"
    if (value < 1024) return "${value.toInt()} B"
    val units = listOf("KiB", "MiB", "GiB", "TiB")
    var scaled = value / 1024
    var unit = 0
    while (scaled >= 1024 && unit < units.lastIndex) { scaled /= 1024; unit++ }
    return String.format(Locale.US, if (scaled >= 100) "%.0f %s" else "%.${digits}f %s", scaled, units[unit])
}

fun formatRate(bytesPerSecond: Double) = "${formatBytes(bytesPerSecond)}/s"

fun parseInstant(value: String?): Instant? = value?.takeIf { it.isNotBlank() }?.let { runCatching { Instant.parse(it) }.getOrNull() }

private val dateTime = DateTimeFormatter.ofLocalizedDateTime(FormatStyle.MEDIUM, FormatStyle.SHORT).withZone(ZoneId.systemDefault())

fun formatTime(value: String?, fallback: String = "—"): String = parseInstant(value)?.let { dateTime.format(it) } ?: fallback

fun formatEpoch(seconds: Long): String = if (seconds <= 0) "—" else dateTime.format(Instant.ofEpochSecond(seconds))

fun relativeTime(value: String?, never: String = "Never"): String {
    val at = parseInstant(value) ?: return never
    val minutes = Duration.between(at, Instant.now()).toMinutes().coerceAtLeast(0)
    return when {
        minutes < 1 -> "Just now"
        minutes < 60 -> "${minutes}m ago"
        minutes < 48 * 60 -> "${minutes / 60}h ago"
        else -> "${minutes / (24 * 60)}d ago"
    }
}

fun formatDuration(seconds: Long): String {
    if (seconds <= 0) return "—"
    val days = seconds / 86_400
    val hours = seconds % 86_400 / 3_600
    val minutes = seconds % 3_600 / 60
    return when {
        days > 0 -> "${days}d ${hours}h"
        hours > 0 -> "${hours}h ${minutes}m"
        else -> "${minutes}m"
    }
}

fun shortId(id: String, n: Int = 12) = id.removePrefix("sha256:").take(n)

/** Status colours shared by every list: green good, amber caution, red problem, grey inactive. */
object Palette {
    val good = Color(0xFF2E9E5B)
    val warn = Color(0xFFD9A400)
    val bad = Color(0xFFD64545)
    val info = Color(0xFF3B82C4)
    val idle = Color(0xFF7A8794)
    val accent = Color(0xFF8B5CF6)

    fun container(state: String) = when (state) {
        "running" -> good
        "paused" -> warn
        "restarting" -> Color(0xFFE67E22)
        "dead" -> bad
        "created" -> info
        else -> idle
    }

    fun device(status: String) = when (status) {
        "healthy" -> good
        "stale" -> warn
        "failed" -> bad
        else -> idle
    }

    fun deployment(status: String) = when (status) {
        "succeeded" -> good
        "failed" -> bad
        "queued", "running" -> warn
        else -> idle
    }
}
