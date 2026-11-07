package network.openvibe.link.ui

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

    Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(20.dp)) {
        Text("This phone", style = MaterialTheme.typography.headlineSmall)
        Text(
            "Paired as a phone on your OpenVibe account. It holds no camera and no screen capture: the only thing it does in the background is keep one connection open.",
            style = MaterialTheme.typography.bodyMedium,
            modifier = Modifier.padding(top = 4.dp, bottom = 16.dp),
        )

        Card(Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                labelled("Device", info?.optString("device_id")?.takeIf { it.isNotEmpty() } ?: "Binding…")
                labelled("Kind", info?.optString("kind") ?: "phone")
                labelled("Account", Store.deviceLabel.ifBlank { "this phone" })
                info?.optString("network")?.takeIf { it.isNotEmpty() }?.let { labelled("Network", it) }
                val node = info?.optString("principal").orEmpty()
                if (node.isNotEmpty()) labelled("Node", node)
            }
        }

        HorizontalDivider(Modifier.padding(vertical = 16.dp))

        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text("Stay connected", style = MaterialTheme.typography.titleMedium)
                Text(
                    "Keep the connection open in the background, with a notification you can always see. Off, the phone connects only while the app is open.",
                    style = MaterialTheme.typography.bodySmall,
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
        Text(
            statusLine(),
            style = MaterialTheme.typography.bodySmall,
            modifier = Modifier.padding(top = 8.dp),
        )

        HorizontalDivider(Modifier.padding(vertical = 16.dp))

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

        HorizontalDivider(Modifier.padding(vertical = 16.dp))

        OutlinedButton(onClick = { confirmUnpair = true }, modifier = Modifier.fillMaxWidth()) {
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

@Composable
private fun labelled(label: String, value: String) {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Text(label, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(0.35f))
        Text(value, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(0.65f))
    }
}

private fun statusLine(): String = when (LinkController.state) {
    "connected" -> "Connected to OpenVibe."
    "revoked" -> LinkController.detail ?: "This phone was unpaired. Pair it again."
    "offline" -> "Offline. Reconnecting while Stay connected is on."
    else -> "Not connected."
}
