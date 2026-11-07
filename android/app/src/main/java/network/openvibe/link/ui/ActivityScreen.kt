package network.openvibe.link.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.History
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import network.openvibe.link.data.ActionItem
import network.openvibe.link.data.Store

/** The words and the dot for each bucket of the Action Center. */
private fun bucketLook(bucket: String): Pair<String, Tone> = when (bucket.lowercase()) {
    "running", "active" -> "Running" to Tone.Ok
    "waiting", "pending" -> "Waiting for you" to Tone.Warn
    "scheduled", "later" -> "Scheduled" to Tone.Neutral
    "failed", "error" -> "Did not finish" to Tone.Bad
    "done", "finished", "succeeded" -> "Done" to Tone.Info
    else -> bucket.replaceFirstChar { it.uppercase() } to Tone.Neutral
}

/** The order the groups read in: what is happening now first, what is done last. */
private val ORDER = listOf("running", "active", "waiting", "pending", "scheduled", "later", "failed", "error", "done", "finished", "succeeded")

/** The Dock's timeline, read-only: everything your agents are doing, from the Action Center, grouped by state. */
@Composable
fun ActivityScreen(preview: List<ActionItem>? = null) {
    LaunchedEffect(Store.signedIn) {
        if (preview == null && Store.signedIn) ActionsRepo.refresh()
    }

    if (preview == null && !Store.signedIn) {
        SignInCard("Activity")
        return
    }

    val items = preview ?: ActionsRepo.activity
    val firstLoad = preview == null && ActionsRepo.loading && ActionsRepo.doc == null
    val groups = items.groupBy { it.bucket.lowercase() }.toList()
        .sortedBy { (b, _) -> ORDER.indexOf(b).let { if (it < 0) ORDER.size else it } }

    LinkPage(title = "Activity", lead = "What your agents are running, waiting on and finished. Answer approvals on their own tab.") {
        ActionsRepo.error?.let { err ->
            item { Text(err, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall) }
        }
        if (firstLoad) {
            skeletonCards(2)
            return@LinkPage
        }
        if (items.isEmpty()) {
            item { EmptyState(Icons.Outlined.History, "Nothing yet", "When an agent starts a task for you, it shows here as it runs, with what it is waiting on.") }
            return@LinkPage
        }
        groups.forEach { (bucket, rows) ->
            val (label, tone) = bucketLook(bucket)
            item(key = "h-$bucket") {
                Row(Modifier.padding(top = 10.dp, bottom = 2.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    StatusDot(tone)
                    Text("$label · ${rows.size}", style = MaterialTheme.typography.labelLarge, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            items(rows, key = { it.service + it.id + (it.updatedAt ?: "") }) { ActivityRow(it) }
        }
    }
}

/** One thing an agent is doing: its sentence, the latest line, and who and when. */
@Composable
fun ActivityRow(item: ActionItem) {
    LinkCard {
        Text(item.title, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.onSurface)
        item.summary?.takeIf { it.isNotBlank() && it != item.title }?.let {
            Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        val who = listOfNotNull(item.agent, (item.updatedAt ?: item.createdAt)?.let { shortTime(it) }).joinToString(" · ")
        if (who.isNotEmpty()) {
            Column(Modifier.fillMaxWidth()) {
                Text(who, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
}
