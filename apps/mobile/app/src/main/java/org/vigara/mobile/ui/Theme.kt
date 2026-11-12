package org.vigara.mobile.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import org.vigara.mobile.ui.common.Palette

// The web app's palette (apps/web/src/theme.ts) as Material colours, so the app and the phone web
// layout read as one product: blue actions, navy accents, a cool grey canvas behind white panels.
// Brand orange is reserved for the logo, never a status or action colour.
private val Light = lightColorScheme(
    primary = Color(0xFF2A6ABC), onPrimary = Color.White,
    primaryContainer = Color(0xFFD4E4F8), onPrimaryContainer = Color(0xFF103C72),
    secondary = Color(0xFF24425F), onSecondary = Color.White,
    secondaryContainer = Color(0xFFE2E9F1), onSecondaryContainer = Color(0xFF122D47),
    tertiary = Color(0xFF1D5FB0), onTertiary = Color.White,
    background = Color(0xFFF5F7FA), onBackground = Color(0xFF0E1A29),
    surface = Color(0xFFFFFFFF), onSurface = Color(0xFF0E1A29),
    surfaceVariant = Color(0xFFEEF2F6), onSurfaceVariant = Color(0xFF6A7587),
    surfaceContainerLowest = Color(0xFFFFFFFF), surfaceContainerLow = Color(0xFFFFFFFF),
    surfaceContainer = Color(0xFFF5F7FA), surfaceContainerHigh = Color(0xFFEEF2F6), surfaceContainerHighest = Color(0xFFE4E9F0),
    outline = Color(0xFFC5CED9), outlineVariant = Color(0xFFE4E9F0),
    error = Color(0xFFC9353A), onError = Color.White, errorContainer = Color(0xFFFDECEC), onErrorContainer = Color(0xFF821F23),
)

private val Dark = darkColorScheme(
    primary = Color(0xFF6AA6EE), onPrimary = Color(0xFF07101A),
    primaryContainer = Color(0xFF174E92), onPrimaryContainer = Color(0xFFD4E4F8),
    secondary = Color(0xFF9DBBDA), onSecondary = Color(0xFF07101A),
    secondaryContainer = Color(0xFF13253A), onSecondaryContainer = Color(0xFFC9D6E4),
    tertiary = Color(0xFF8ABAF2), onTertiary = Color(0xFF07101A),
    background = Color(0xFF0A121C), onBackground = Color(0xFFE7EDF4),
    surface = Color(0xFF101B28), onSurface = Color(0xFFE7EDF4),
    surfaceVariant = Color(0xFF1C2D42), onSurfaceVariant = Color(0xFFA7B4C4),
    surfaceContainerLowest = Color(0xFF0A121C), surfaceContainerLow = Color(0xFF101B28),
    surfaceContainer = Color(0xFF13253A), surfaceContainerHigh = Color(0xFF1C2D42), surfaceContainerHighest = Color(0xFF22334A),
    outline = Color(0xFF2E4361), outlineVariant = Color(0xFF22334A),
    error = Color(0xFFF0686C), onError = Color(0xFF07101A), errorContainer = Color(0xFF3A1416), onErrorContainer = Color(0xFFF9D2D3),
)

@Composable
fun VigaraTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    // Status colours are plain values used outside composition too, so they follow this flag.
    Palette.dark = dark
    MaterialTheme(colorScheme = if (dark) Dark else Light, content = content)
}
