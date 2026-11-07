package org.vigara.mobile.ui.common

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.unit.dp
import org.vigara.mobile.data.Series
import java.time.Instant

val seriesColors = listOf(Color(0xFF2E9E5B), Color(0xFF3B82C4), Color(0xFFE67E22), Color(0xFF8B5CF6), Color(0xFFD64545))

/** A minimal multi-series line chart: gaps (null values) break the line; a shared y scale per chart. */
@Composable
fun LineChart(series: List<Series>, unit: String, modifier: Modifier = Modifier) {
    val points = series.map { s -> s.points.mapNotNull { p -> p.value?.let { v -> runCatching { Instant.parse(p.at).epochSecond.toDouble() }.getOrNull()?.let { x -> x to v } } } }
    val all = points.flatten()
    if (all.size < 2) {
        Text("Not enough data in this range.", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = modifier.padding(8.dp))
        return
    }
    val minX = all.minOf { it.first }
    val maxX = all.maxOf { it.first }
    val minY = minOf(0.0, all.minOf { it.second })
    val maxY = all.maxOf { it.second }.let { if (it <= minY) minY + 1 else it }
    val grid = MaterialTheme.colorScheme.outlineVariant
    Column(modifier) {
        Row(Modifier.fillMaxWidth()) {
            Text(formatMetricValue(maxY, unit), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Canvas(Modifier.fillMaxWidth().height(150.dp)) {
            val w = size.width
            val h = size.height
            for (i in 0..2) drawLine(grid, Offset(0f, h * i / 2f), Offset(w, h * i / 2f), strokeWidth = 1f)
            points.forEachIndexed { index, line ->
                val path = Path()
                var started = false
                line.forEach { (x, y) ->
                    val px = if (maxX == minX) 0f else ((x - minX) / (maxX - minX) * w).toFloat()
                    val py = (h - (y - minY) / (maxY - minY) * h).toFloat()
                    if (!started) { path.moveTo(px, py); started = true } else path.lineTo(px, py)
                }
                drawPath(path, seriesColors[index % seriesColors.size], style = Stroke(width = 3f))
            }
        }
        Row(Modifier.fillMaxWidth()) {
            Text(formatMetricValue(minY, unit), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

/** Mirrors the web UI's unit handling (seconds, bytes, byte rates, everything else as "value unit"). */
fun formatMetricValue(value: Double, unit: String): String = when (unit) {
    "s" -> formatDuration(value.toLong())
    "By", "bytes" -> formatBytes(value)
    "By/s", "bytes/s" -> formatRate(value)
    "%" -> "${trim(value)}%"
    "" -> trim(value)
    else -> "${trim(value)} $unit"
}

private fun trim(value: Double): String = if (value >= 100) "%.0f".format(value) else "%.2f".format(value).trimEnd('0').trimEnd('.')
