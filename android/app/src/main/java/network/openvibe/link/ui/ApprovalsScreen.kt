package network.openvibe.link.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.DeleteOutline
import androidx.compose.material.icons.outlined.Fingerprint
import androidx.compose.material.icons.outlined.Key
import androidx.compose.material.icons.outlined.Payments
import androidx.compose.material.icons.outlined.PersonAddAlt
import androidx.compose.material.icons.outlined.TaskAlt
import androidx.compose.material.icons.outlined.Tune
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.fragment.app.FragmentActivity
import kotlinx.coroutines.launch
import network.openvibe.link.approval.ApprovalCategory
import network.openvibe.link.approval.ApprovalRules
import network.openvibe.link.approval.Biometrics
import network.openvibe.link.data.ActionItem
import network.openvibe.link.data.Store

/** Each category's tone and icon: money amber, deleting red, passwords violet, a new recipient blue. */
private fun ApprovalCategory.tone(): Tone = when (this) {
    ApprovalCategory.MONEY -> Tone.Warn
    ApprovalCategory.DELETE -> Tone.Bad
    ApprovalCategory.PASSWORD -> Tone.Secret
    ApprovalCategory.NEW_RECIPIENT -> Tone.Info
    ApprovalCategory.OTHER -> Tone.Neutral
}

private fun ApprovalCategory.icon(): ImageVector = when (this) {
    ApprovalCategory.MONEY -> Icons.Outlined.Payments
    ApprovalCategory.DELETE -> Icons.Outlined.DeleteOutline
    ApprovalCategory.PASSWORD -> Icons.Outlined.Key
    ApprovalCategory.NEW_RECIPIENT -> Icons.Outlined.PersonAddAlt
    ApprovalCategory.OTHER -> Icons.Outlined.Tune
}

/**
 * Actor's pending approvals for the person. Approve or decline; money, passwords, deletions and a send to a new
 * recipient ask for a fingerprint, a face or the device credential first, and only then send the answer. Each card
 * leads with what it is about (a tinted chip), then the agent's sentence, then who asked and when, then the answer.
 */
@Composable
fun ApprovalsScreen(preview: List<ActionItem>? = null) {
    val context = LocalContext.current
    val activity = context as? FragmentActivity
    val scope = rememberCoroutineScope()
    var working by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(Store.signedIn) {
        if (preview == null && Store.signedIn) ActionsRepo.refresh()
    }

    if (preview == null && !Store.signedIn) {
        SignInCard("Approvals")
        return
    }

    val waiting = preview ?: ActionsRepo.approvals
    val firstLoad = preview == null && ActionsRepo.loading && ActionsRepo.doc == null

    LinkPage(
        title = "Approvals",
        lead = when {
            firstLoad -> "Asking your agents what is waiting…"
            waiting.isEmpty() -> null
            waiting.size == 1 -> "One thing is waiting for your answer."
            else -> "${waiting.size} things are waiting for your answer."
        },
    ) {
        ActionsRepo.error?.let { err ->
            item { Text(err, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall) }
        }
        if (firstLoad) {
            skeletonCards(3)
            return@LinkPage
        }
        if (waiting.isEmpty()) {
            item {
                EmptyState(
                    Icons.Outlined.TaskAlt,
                    "Nothing is waiting for you",
                    "When an agent wants to pay, delete, sign in or write to someone new, it asks here first.",
                )
            }
            return@LinkPage
        }

        items(waiting, key = { it.service + it.id }) { item ->
            val category = ApprovalRules.classify(item.category, item.kind, item.title, item.summary)
            val decide: (Boolean) -> Unit = { approve ->
                val send = {
                    working = item.id
                    scope.launch {
                        try {
                            ActionsRepo.decide(item, approve)
                        } catch (e: Exception) {
                            Messages.show(e.message ?: "The answer was not sent.")
                        } finally {
                            working = null
                        }
                    }
                    Unit
                }
                if (category.needsBiometrics && activity != null) {
                    Biometrics.confirm(
                        activity,
                        "${if (approve) category.label else "Declining"} · ${item.title}",
                        onSuccess = { send() },
                        onError = { Messages.show(it) },
                    )
                } else {
                    send()
                }
            }
            LinkCard(accent = if (category.needsBiometrics) toneColor(category.tone()) else null) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    ToneChip(category.label, category.tone(), category.icon())
                    if (category.needsBiometrics) {
                        Icon(Icons.Outlined.Fingerprint, contentDescription = "Asks for your fingerprint or face", tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(18.dp))
                    }
                }
                Text(item.title, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.onSurface)
                item.summary?.takeIf { it.isNotBlank() && it != item.title }?.let {
                    Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                val who = listOfNotNull(item.agent, item.createdAt?.let { shortTime(it) }).joinToString(" · ")
                if (who.isNotEmpty()) {
                    Text(who, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                Row(Modifier.fillMaxWidth().padding(top = 6.dp), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    Button(
                        enabled = working == null,
                        onClick = { decide(true) },
                        modifier = Modifier.weight(1f).heightIn(min = 48.dp),
                    ) { Text(if (working == item.id) "Sending…" else "Approve", fontWeight = FontWeight.Bold) }
                    OutlinedButton(
                        enabled = working == null,
                        onClick = { decide(false) },
                        modifier = Modifier.weight(1f).heightIn(min = 48.dp),
                    ) { Text("Decline") }
                }
            }
        }
    }
}

/** "2026-11-07T15:00:00.000Z" → "7 Nov 15:00". A value the formatter cannot read is shown as it came. */
fun shortTime(iso: String): String = try {
    val instant = java.time.Instant.parse(iso)
    val local = instant.atZone(java.time.ZoneId.systemDefault())
    java.time.format.DateTimeFormatter.ofPattern("d MMM HH:mm").format(local)
} catch (e: Exception) {
    iso.take(16).replace('T', ' ')
}
