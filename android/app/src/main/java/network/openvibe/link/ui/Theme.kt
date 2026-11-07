package network.openvibe.link.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import network.openvibe.theme.VibeDarkColorScheme
import network.openvibe.theme.VibeLightColorScheme

/** The rest of OpenVibe, on the phone: the generated Vibe theme (openvibe-shared's native export), never hand-drawn. */
@Composable
fun LinkTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = if (isSystemInDarkTheme()) VibeDarkColorScheme else VibeLightColorScheme,
        content = content,
    )
}
