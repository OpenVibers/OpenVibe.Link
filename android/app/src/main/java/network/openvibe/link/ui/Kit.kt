package network.openvibe.link.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/*
 * Link's small kit (Opus, 11-07): the pieces every screen is made of, in OpenVibe's look — a page with its title and one
 * line under it, a card on the raised surface with a hairline, a chip whose tone says how much a thing matters, a
 * status dot, a still skeleton while the first answer loads (no spinning ring: the owner's rule), and an empty state
 * that says what will appear and why. Colours come only from the theme (LinkTheme).
 */

/** How much a thing matters: the tone of a chip or a dot. */
enum class Tone { Neutral, Info, Ok, Warn, Bad, Secret }

@Composable
fun toneColor(tone: Tone): Color = when (tone) {
    Tone.Neutral -> MaterialTheme.colorScheme.onSurfaceVariant
    Tone.Info -> MaterialTheme.colorScheme.tertiary
    Tone.Ok -> Color(0xFF22C55E)
    Tone.Warn -> Color(0xFFF59E0B)
    Tone.Bad -> MaterialTheme.colorScheme.error
    Tone.Secret -> Color(0xFFA78BFA)
}

/** A screen: its title and one line, then the content, as a list that scrolls under the bar. */
@Composable
fun LinkPage(title: String, lead: String?, modifier: Modifier = Modifier, content: LazyListScope.() -> Unit) {
    LazyColumn(
        modifier = modifier.fillMaxSize().background(MaterialTheme.colorScheme.background),
        contentPadding = PaddingValues(start = 20.dp, end = 20.dp, top = 24.dp, bottom = 32.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item {
            Column(Modifier.padding(bottom = 6.dp)) {
                Text(title, style = MaterialTheme.typography.headlineMedium, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.onBackground)
                if (!lead.isNullOrBlank()) {
                    Text(lead, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 6.dp))
                }
            }
        }
        content()
    }
}

/** A card on the raised surface, with a hairline and the house's 18 dp corners. */
@Composable
fun LinkCard(modifier: Modifier = Modifier, accent: Color? = null, content: @Composable ColumnScope.() -> Unit) {
    Surface(
        modifier = modifier.fillMaxWidth(),
        shape = RoundedCornerShape(18.dp),
        color = MaterialTheme.colorScheme.surface,
        border = BorderStroke(1.dp, accent?.copy(alpha = 0.45f) ?: MaterialTheme.colorScheme.outlineVariant),
    ) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp), content = content)
    }
}

/** A small pill: an icon and a word, tinted by its tone. */
@Composable
fun ToneChip(text: String, tone: Tone, icon: ImageVector? = null) {
    val c = toneColor(tone)
    Row(
        Modifier.background(c.copy(alpha = 0.14f), RoundedCornerShape(999.dp)).padding(horizontal = 10.dp, vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        if (icon != null) Icon(icon, contentDescription = null, tint = c, modifier = Modifier.size(14.dp))
        Text(text, color = c, fontSize = 12.sp, fontWeight = FontWeight.Bold)
    }
}

/** A dot that says the state of a thing (green running, amber waiting, grey later). Still: it never pulses. */
@Composable
fun StatusDot(tone: Tone, modifier: Modifier = Modifier) {
    Box(modifier.size(10.dp).background(toneColor(tone), CircleShape))
}

/** The first answer is on its way: still blocks where cards will be. */
fun LazyListScope.skeletonCards(count: Int = 3) {
    items(count) {
        LinkCard {
            Box(Modifier.fillMaxWidth(0.7f).height(16.dp).background(MaterialTheme.colorScheme.surfaceVariant, RoundedCornerShape(6.dp)))
            Box(Modifier.fillMaxWidth(0.9f).height(12.dp).background(MaterialTheme.colorScheme.surfaceVariant, RoundedCornerShape(6.dp)))
            Spacer(Modifier.height(4.dp))
            Box(Modifier.width(96.dp).height(12.dp).background(MaterialTheme.colorScheme.surfaceVariant, RoundedCornerShape(6.dp)))
        }
    }
}

/** Nothing here yet: an icon, a sentence that says what will appear, and why it is empty. */
@Composable
fun EmptyState(icon: ImageVector, title: String, body: String) {
    Column(
        Modifier.fillMaxWidth().padding(vertical = 36.dp, horizontal = 12.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Box(
            Modifier.size(64.dp).background(MaterialTheme.colorScheme.primary.copy(alpha = 0.12f), CircleShape),
            contentAlignment = Alignment.Center,
        ) { Icon(icon, contentDescription = null, tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(30.dp)) }
        Text(title, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.onBackground)
        Text(body, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = androidx.compose.ui.text.style.TextAlign.Center)
    }
}

/** A label and its value on one line, the label muted. */
@Composable
fun LabelledRow(label: String, value: String) {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Text(label, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(0.38f))
        Text(value, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurface, modifier = Modifier.weight(0.62f))
    }
}
