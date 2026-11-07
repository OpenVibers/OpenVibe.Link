package network.openvibe.link.ui

import android.net.Uri
import android.widget.Toast
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.History
import androidx.compose.material.icons.outlined.Login
import androidx.compose.material.icons.outlined.PhoneAndroid
import androidx.compose.material.icons.outlined.TaskAlt
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import network.openvibe.link.data.Servers
import network.openvibe.link.data.Session
import network.openvibe.link.data.Store

/** The app: pair first, then the three screens of a paired phone. */
@Composable
fun LinkApp() {
    val context = LocalContext.current

    val authUri = Incoming.authUri
    LaunchedEffect(authUri) {
        val raw = authUri ?: return@LaunchedEffect
        try {
            Session.complete(Uri.parse(raw))
            Messages.show("Signed in.")
            ActionsRepo.refresh()
        } catch (e: Exception) {
            Messages.show(e.message ?: "Sign-in failed.")
        }
        Incoming.authUri = null
    }

    LaunchedEffect(Messages.text) {
        val message = Messages.text ?: return@LaunchedEffect
        Toast.makeText(context, message, Toast.LENGTH_LONG).show()
        Messages.text = null
    }

    Column(Modifier.fillMaxSize()) {
        // A debug build pointed at another server says so on every screen: nothing here is production.
        if (Servers.isDebugOverride) {
            Text(
                "Debug server · ${Servers.host}",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onTertiaryContainer,
                modifier = Modifier
                    .fillMaxWidth()
                    .background(MaterialTheme.colorScheme.tertiaryContainer)
                    .padding(horizontal = 16.dp, vertical = 6.dp),
            )
        }
        Box(Modifier.weight(1f)) {
            if (Store.paired == null) {
                PairScreen(initialCode = Incoming.pairCode)
            } else {
                MainScreen()
            }
        }
    }
}

@Composable
private fun MainScreen() {
    var tab by rememberSaveable { mutableIntStateOf(0) }
    val tabs = listOf("Approvals" to Icons.Outlined.TaskAlt, "Activity" to Icons.Outlined.History, "This phone" to Icons.Outlined.PhoneAndroid)
    Scaffold(
        bottomBar = {
            NavigationBar(containerColor = MaterialTheme.colorScheme.surface) {
                tabs.forEachIndexed { index, (label, icon) ->
                    NavigationBarItem(
                        selected = tab == index,
                        onClick = { tab = index },
                        icon = { Icon(icon, contentDescription = null) },
                        label = { Text(label) },
                    )
                }
            }
        },
    ) { padding ->
        Box(Modifier.fillMaxSize().padding(padding)) {
            when (tab) {
                0 -> ApprovalsScreen()
                1 -> ActivityScreen()
                else -> PhoneScreen()
            }
        }
    }
}

/** A session and a paired phone are both needed for the two read screens: one card, one button. */
@Composable
fun SignInCard(title: String = "Your OpenVibe account") {
    val context = LocalContext.current
    LinkPage(title = title, lead = null) {
        item {
            LinkCard {
                EmptyState(
                    Icons.Outlined.Login,
                    "Sign in to see what your agents need",
                    "The phone keeps a token from OpenVibe.Network, sealed with a key only this phone's Keystore holds.",
                )
                Button(
                    onClick = { if (!Session.signIn(context)) Messages.show("No browser answered. Install one and try again.") },
                    modifier = Modifier.fillMaxWidth(),
                ) { Text("Sign in to OpenVibe") }
            }
        }
    }
}
