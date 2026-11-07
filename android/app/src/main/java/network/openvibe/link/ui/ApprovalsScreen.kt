package network.openvibe.link.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
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
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.fragment.app.FragmentActivity
import kotlinx.coroutines.launch
import network.openvibe.link.approval.ApprovalRules
import network.openvibe.link.approval.Biometrics
import network.openvibe.link.data.ActionItem
import network.openvibe.link.data.Store

/**
 * Actor's pending approvals for the person. Approve or decline; money, passwords, deletions and a send to a new
 * recipient ask for a fingerprint, a face or the device credential first, and only then send the answer.
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

    LazyColumn(contentPadding = PaddingValues(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Text("Approvals", style = MaterialTheme.typography.headlineSmall)
            Text(
                if (waiting.isEmpty()) "Nothing is waiting for you." else "${waiting.size} waiting for your answer.",
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.padding(top = 4.dp),
            )
            ActionsRepo.error?.let {
                Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 8.dp))
            }
        }

        if (ActionsRepo.loading && ActionsRepo.doc == null) {
            item { CircularProgressIndicator(Modifier.padding(top = 24.dp)) }
        }

        items(waiting, key = { it.service + it.id }) { item ->
            val category = ApprovalRules.classify(item.category, item.kind, item.title, item.summary)
            Card(Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp)) {
                    Text(item.title, style = MaterialTheme.typography.titleMedium)
                    item.summary?.takeIf { it.isNotBlank() && it != item.title }?.let {
                        Text(it, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(top = 6.dp))
                    }
                    Row(Modifier.padding(top = 10.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(category.label, style = MaterialTheme.typography.labelMedium)
                        item.agent?.let { Text(it, style = MaterialTheme.typography.labelMedium) }
                        item.createdAt?.let { Text(shortTime(it), style = MaterialTheme.typography.labelMedium) }
                    }
                    Row(Modifier.padding(top = 12.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        Button(
                            enabled = working == null,
                            onClick = {
                                val send: (Boolean) -> Unit = { approve ->
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
                                }
                                if (category.needsBiometrics && activity != null) {
                                    Biometrics.confirm(
                                        activity,
                                        "${category.label} · ${item.title}",
                                        onSuccess = { send(true) },
                                        onError = { Messages.show(it) },
                                    )
                                } else {
                                    send(true)
                                }
                            },
                        ) { Text(if (working == item.id) "Sending…" else "Approve") }
                        OutlinedButton(
                            enabled = working == null,
                            onClick = {
                                val send: (Boolean) -> Unit = { _ ->
                                    working = item.id
                                    scope.launch {
                                        try {
                                            ActionsRepo.decide(item, false)
                                        } catch (e: Exception) {
                                            Messages.show(e.message ?: "The answer was not sent.")
                                        } finally {
                                            working = null
                                        }
                                    }
                                }
                                if (category.needsBiometrics && activity != null) {
                                    Biometrics.confirm(
                                        activity,
                                        "Declining · ${item.title}",
                                        onSuccess = { send(false) },
                                        onError = { Messages.show(it) },
                                    )
                                } else {
                                    send(false)
                                }
                            },
                        ) { Text("Decline") }
                    }
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

/** The Activity screen's rows, kept beside the approval rules they mirror. */
@Composable
fun ActivityRow(item: ActionItem) {
    Column(Modifier.fillMaxWidth().padding(vertical = 8.dp)) {
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(item.bucket, style = MaterialTheme.typography.labelSmall)
            item.agent?.let { Text(it, style = MaterialTheme.typography.labelSmall) }
        }
        Text(item.title, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.padding(top = 2.dp))
        item.summary?.takeIf { it.isNotBlank() && it != item.title }?.let {
            Text(it, style = MaterialTheme.typography.bodySmall)
        }
        (item.updatedAt ?: item.createdAt)?.let {
            Text(shortTime(it), style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 2.dp))
        }
    }
}
