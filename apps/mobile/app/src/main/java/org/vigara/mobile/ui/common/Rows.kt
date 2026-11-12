package org.vigara.mobile.ui.common

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** The small coloured dot that leads every status, as on the web. */
@Composable
fun StatusDot(color: Color, modifier: Modifier = Modifier) {
    Box(modifier.size(8.dp).background(color, CircleShape))
}

/** Dot and word: the status style used across the app instead of filled badges. */
@Composable
fun StatusText(text: String, color: Color, modifier: Modifier = Modifier) {
    Row(modifier, verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        StatusDot(color)
        Text(text, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurface, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/**
 * One row of a list, in two lines: a status dot and name, the detail beneath it, and the key
 * figure on the right. Rows sit edge to edge on the surface with a hairline between them.
 */
@Composable
fun TwoLineRow(
    title: String,
    subtitle: String? = null,
    dot: Color? = null,
    trailing: String? = null,
    trailingDetail: String? = null,
    trailingColor: Color? = null,
    subtitleColor: Color? = null,
    onClick: (() -> Unit)? = null,
) {
    Column(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surface)) {
        Row(
            Modifier.fillMaxWidth().let { if (onClick != null) it.clickable(onClick = onClick) else it }.padding(horizontal = 16.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (dot != null) { StatusDot(dot); Box(Modifier.size(10.dp)) }
            Column(Modifier.weight(1f)) {
                Text(title, style = MaterialTheme.typography.bodyLarge, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (!subtitle.isNullOrBlank()) Text(
                    subtitle, style = MaterialTheme.typography.bodySmall, color = subtitleColor ?: MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 2, overflow = TextOverflow.Ellipsis,
                )
            }
            if (trailing != null || trailingDetail != null) Column(Modifier.padding(start = 12.dp), horizontalAlignment = Alignment.End) {
                if (trailing != null) Text(trailing, style = MaterialTheme.typography.bodyMedium, color = trailingColor ?: MaterialTheme.colorScheme.onSurface, maxLines = 1)
                if (trailingDetail != null) Text(trailingDetail, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1)
            }
        }
        HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
    }
}

/** A small uppercase heading above a group of rows, with an optional count. */
@Composable
fun ListHeader(text: String, count: Int? = null, modifier: Modifier = Modifier) {
    Row(modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text.uppercase(), fontSize = 11.sp, letterSpacing = 0.8.sp, fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (count != null) Text("  $count", fontSize = 11.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
    HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
}
