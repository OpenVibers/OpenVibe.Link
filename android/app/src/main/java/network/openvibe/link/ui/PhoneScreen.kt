package network.openvibe.link.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.PhoneAndroid
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import network.openvibe.link.data.LinkController
import network.openvibe.link.data.Session
import network.openvibe.link.data.Store
import network.openvibe.link.service.StayConnectedService
import org.json.JSONObject

/** "This phone": what it is paired as, Stay connected, sign-in, and Unpair. */
@Composable
fun PhoneScreen(previewInfo: JSONObject? = null, preview: Boolean = false) {
    val context = LocalContext.current
    val info = previewInfo ?: remember(Store.paired) { LinkController.info()?.let { runCatching { JSONObject(it) }.getOrNull() } }
    var confirmUnpair by remember { mutableStateOf(false) }

    var showIds by remember { mutableStateOf(false) }
    Column(
        Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.background).verticalScroll(rememberScrollState()).padding(horizontal = 20.dp, vertical = 24.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Text("This phone", style = MaterialTheme.typography.headlineMedium, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.onBackground)
        Text(
            "One of your devices. It has no camera or screen access: in the background it only keeps one connection open.",
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )

        LinkCard {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                Box(
                    Modifier.size(48.dp).background(MaterialTheme.colorScheme.primary.copy(alpha = 0.14f), RoundedCornerShape(14.dp)),
                    contentAlignment = Alignment.Center,
                ) { Icon(Icons.Outlined.PhoneAndroid, contentDescription = null, tint = MaterialTheme.colorScheme.primary) }
                Column(Modifier.weight(1f)) {
                    Text(Store.deviceLabel.ifBlank { "This phone" }, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.onSurface)
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        StatusDot(if (LinkController.state == "connected") Tone.Ok else if (LinkController.state == "revoked") Tone.Bad else Tone.Neutral)
                        Text(statusLine(), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }
            TextButton(onClick = { showIds = !showIds }, contentPadding = PaddingValues(0.dp)) {
                Text(if (showIds) "Hide the details" else "Details: device and node ids")
            }
            if (showIds) {
                LabelledRow("Device", info?.optString("device_id")?.takeIf { it.isNotEmpty() } ?: "Binding…")
                LabelledRow("Kind", info?.optString("kind") ?: "phone")
                info?.optString("network")?.takeIf { it.isNotEmpty() }?.let { LabelledRow("Network", it) }
                val node = info?.optString("principal").orEmpty()
                if (node.isNotEmpty()) LabelledRow("Node", node)
            }
        }

        LinkCard {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Column(Modifier.weight(1f)) {
                Text("Stay connected", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.onSurface)
                Text(
                    "Approvals arrive even when the app is closed, with a notification you can always see. Off, the phone connects only while the app is open.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Switch(
                checked = Store.stayConnected,
                onCheckedChange = { on ->
                    Store.keepConnected(on)
                    if (on) StayConnectedService.start(context) else StayConnectedService.stop(context)
                },
            )
        }
        }

        if (Store.signedIn) {
            Text("Signed in to your OpenVibe account.", style = MaterialTheme.typography.bodyMedium)
            OutlinedButton(
                onClick = {
                    Session.signOut()
                    ActionsRepo.clear()
                },
            ) { Text("Sign out") }
        } else {
            Button(
                onClick = {
                    if (!Session.signIn(context)) Messages.show("No browser answered. Install one and try again.")
                },
                modifier = Modifier.fillMaxWidth(),
            ) { Text("Sign in to OpenVibe") }
        }

        OutlinedButton(
            onClick = { confirmUnpair = true },
            modifier = Modifier.fillMaxWidth().padding(top = 8.dp),
            colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
            border = BorderStroke(1.dp, MaterialTheme.colorScheme.error.copy(alpha = 0.5f)),
        ) {
            Text("Unpair this phone")
        }
        Text(
            "Unpairing forgets the phone's credential on this device. Remove the device on openvibe.bot as well if you want it gone from your account.",
            style = MaterialTheme.typography.bodySmall,
            modifier = Modifier.padding(top = 8.dp),
        )
    }

    if (confirmUnpair) {
        AlertDialog(
            onDismissRequest = { confirmUnpair = false },
            title = { Text("Unpair this phone?") },
            text = { Text("It stops receiving approvals and can no longer send files. Nothing else on the phone is deleted.") },
            confirmButton = {
                TextButton(onClick = {
                    confirmUnpair = false
                    StayConnectedService.stop(context)
                    Store.keepConnected(false)
                    LinkController.stop()
                    Session.signOut()
                    ActionsRepo.clear()
                    Store.forgetPairing()
                }) { Text("Unpair") }
            },
            dismissButton = {
                TextButton(onClick = { confirmUnpair = false }) { Text("Keep it") }
            },
        )
    }
}


private fun statusLine(): String = when (LinkController.state) {
    "connected" -> "Connected to OpenVibe."
    "revoked" -> LinkController.detail ?: "This phone was unpaired. Pair it again."
    "offline" -> "Offline. Reconnecting while Stay connected is on."
    else -> "Not connected."
}
