package network.openvibe.link.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import network.openvibe.theme.VibeDarkColorScheme
import network.openvibe.theme.VibeDarkTokens
import network.openvibe.theme.VibeLightColorScheme
import network.openvibe.theme.VibeLightTokens

/**
 * The rest of OpenVibe, on the phone: the generated Vibe theme (openvibe-shared's native export), never hand-drawn.
 *
 * Material 3's container roles (cards, the bottom bar and its selected pill, dialogs, chips) are not in the export.
 * Left alone they fall back to Material's own lavender, which is what the first screens showed. Here they take the
 * Vibe surfaces, so nothing on screen is a colour OpenVibe does not use.
 */
@Composable
fun LinkTheme(dark: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    val scheme = if (dark) {
        VibeDarkColorScheme.copy(
            surfaceContainerLowest = VibeDarkTokens.ColorBgBase,
            surfaceContainerLow = VibeDarkTokens.ColorBgRaised,
            surfaceContainer = VibeDarkTokens.ColorBgRaised,
            surfaceContainerHigh = VibeDarkTokens.ColorBgSurface,
            surfaceContainerHighest = VibeDarkTokens.ColorBgSubtle,
            surfaceBright = VibeDarkTokens.ColorBgHover,
            surfaceDim = VibeDarkTokens.ColorBgBase,
            surfaceTint = VibeDarkTokens.ColorBgRaised,
            secondaryContainer = VibeDarkTokens.ColorBgHover,
            onSecondaryContainer = VibeDarkTokens.ColorAccentLight,
            inverseSurface = VibeDarkTokens.ColorTextPrimary,
            inverseOnSurface = VibeDarkTokens.ColorBgBase,
        )
    } else {
        VibeLightColorScheme.copy(
            surfaceContainerLowest = VibeLightTokens.ColorBgRaised,
            surfaceContainerLow = VibeLightTokens.ColorBgRaised,
            surfaceContainer = VibeLightTokens.ColorBgRaised,
            surfaceContainerHigh = VibeLightTokens.ColorBgSurface,
            surfaceContainerHighest = VibeLightTokens.ColorBgSubtle,
            surfaceBright = VibeLightTokens.ColorBgRaised,
            surfaceDim = VibeLightTokens.ColorBgSubtle,
            surfaceTint = VibeLightTokens.ColorBgRaised,
            secondaryContainer = VibeLightTokens.ColorBgHover,
            onSecondaryContainer = VibeLightTokens.ColorAccentStrong,
            inverseSurface = VibeLightTokens.ColorTextPrimary,
            inverseOnSurface = VibeLightTokens.ColorBgRaised,
        )
    }
    MaterialTheme(colorScheme = scheme, content = content)
}
