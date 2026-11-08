package network.openvibe.link.ui

import android.Manifest
import android.content.pm.PackageManager
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import kotlinx.coroutines.launch
import org.json.JSONObject
import java.io.File
import java.util.UUID
import network.openvibe.link.approval.ApprovalCategory
import network.openvibe.link.approval.ApprovalRules
import network.openvibe.link.approval.Biometrics
import network.openvibe.link.data.ActionItem
import network.openvibe.link.data.Api
import network.openvibe.link.data.DeviceLocation
import network.openvibe.link.data.LinkController
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
    var capture by remember { mutableStateOf<Pair<ActionItem, File>?>(null) }
    var locationRequest by remember { mutableStateOf<Pair<ActionItem, Boolean>?>(null) }

    val camera = rememberLauncherForActivityResult(ActivityResultContracts.TakePicture()) { saved ->
        val pending = capture
        capture = null
        if (pending != null) {
            val (item, file) = pending
            if (saved) {
                working = item.id
                scope.launch {
                    try {
                        val uri = FileProvider.getUriForFile(context, "${context.packageName}.devicephotos", file)
                        val mediaId = Api.uploadPhoto(item, context, uri)
                        Api.answerDevice(item, Api.photoAnswer(mediaId))
                        ActionsRepo.refresh()
                        Messages.show("Your photo was shared once.")
                    } catch (e: Exception) {
                        Messages.show(e.message ?: "The photo was not shared.")
                    } finally {
                        file.delete()
                        working = null
                    }
                }
            } else file.delete()
        }
    }
    val locationPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { grants ->
        val pending = locationRequest
        locationRequest = null
        if (pending != null) {
            val (item, precise) = pending
            val coarse = grants[Manifest.permission.ACCESS_COARSE_LOCATION] == true ||
                ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_COARSE_LOCATION) == PackageManager.PERMISSION_GRANTED
            val fine = grants[Manifest.permission.ACCESS_FINE_LOCATION] == true ||
                ContextCompat.checkSelfPermission(context, Manifest.permission.ACCESS_FINE_LOCATION) == PackageManager.PERMISSION_GRANTED
            if (!coarse || (precise && !fine)) {
                Messages.show(if (precise) "Precise location was not allowed. You can choose approximate sharing." else "Location was not allowed.")
            } else {
                working = item.id
                scope.launch {
                    try {
                        val point = DeviceLocation.once(context, precise)
                        Api.answerDevice(item, Api.locationAnswer(point, precise))
                        ActionsRepo.refresh()
                        Messages.show("Your location was shared once.")
                    } catch (e: Exception) {
                        Messages.show(e.message ?: "The location was not shared.")
                    } finally { working = null }
                }
            }
        }
    }

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
            val deviceKind = item.deviceKind
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
                    ToneChip(if (deviceKind == "camera") "Photo request" else if (deviceKind == "location") "Location request" else category.label,
                        if (deviceKind != null) Tone.Info else category.tone(), category.icon())
                    if (category.needsBiometrics) {
                        Icon(Icons.Outlined.Fingerprint, contentDescription = "Asks for your fingerprint or face", tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(18.dp))
                    }
                }
                Text(item.title, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.onSurface)
                (if (deviceKind != null) item.deviceWhy else item.summary)?.takeIf { it.isNotBlank() && it != item.title }?.let {
                    Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                val who = listOfNotNull(item.agent, item.createdAt?.let { shortTime(it) }).joinToString(" · ")
                if (who.isNotEmpty()) {
                    Text(who, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                if (deviceKind != null && preview == null && !LinkController.connected) {
                    Text("This phone is offline. Connect it before answering; you can use the approval page in a browser.",
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
                }
                if (deviceKind == "camera") {
                    Button(enabled = working == null && (preview != null || LinkController.connected),
                        onClick = {
                            val folder = File(context.cacheDir, "device-photos").apply { mkdirs() }
                            val file = File(folder, "${UUID.randomUUID()}.jpg")
                            val uri = FileProvider.getUriForFile(context, "${context.packageName}.devicephotos", file)
                            capture = item to file
                            camera.launch(uri)
                        }, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) { Text("Take a photo") }
                } else if (deviceKind == "location") {
                    Button(enabled = working == null && (preview != null || LinkController.connected),
                        onClick = {
                            locationRequest = item to false
                            locationPermission.launch(arrayOf(Manifest.permission.ACCESS_COARSE_LOCATION))
                        }, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) { Text("Share where I am once") }
                    if (item.preciseRequested) {
                        OutlinedButton(enabled = working == null && (preview != null || LinkController.connected),
                            onClick = {
                                locationRequest = item to true
                                locationPermission.launch(arrayOf(Manifest.permission.ACCESS_COARSE_LOCATION, Manifest.permission.ACCESS_FINE_LOCATION))
                            }, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) { Text("Share precise location once") }
                    }
                }
                if (deviceKind != null) {
                    OutlinedButton(enabled = working == null, onClick = {
                        working = item.id
                        scope.launch {
                            try {
                                Api.answerDevice(item, JSONObject().put("decision", "decline"))
                                ActionsRepo.refresh()
                            } catch (e: Exception) { Messages.show(e.message ?: "The answer was not sent.") }
                            finally { working = null }
                        }
                    }, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) { Text("Decline") }
                } else Row(Modifier.fillMaxWidth().padding(top = 6.dp), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
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
