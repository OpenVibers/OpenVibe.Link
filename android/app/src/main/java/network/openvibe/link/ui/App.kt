package network.openvibe.link.ui

import android.net.Uri
import android.widget.Toast
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
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

    if (Store.paired == null) {
        PairScreen(initialCode = Incoming.pairCode)
    } else {
        MainScreen()
    }
}

@Composable
private fun MainScreen() {
    var tab by rememberSaveable { mutableIntStateOf(0) }
    val tabs = listOf("Approvals", "Activity", "This phone")
    Scaffold(
        bottomBar = {
            NavigationBar {
                tabs.forEachIndexed { index, label ->
                    NavigationBarItem(
                        selected = tab == index,
                        onClick = { tab = index },
                        icon = { Text(if (tab == index) "●" else "○") },
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

/** A session and a paired phone are both needed for the two read screens. */
@Composable
fun SignInCard(title: String = "Your OpenVibe account") {
    val context = LocalContext.current
    Column(Modifier.padding(20.dp)) {
        Text(title, style = MaterialTheme.typography.titleMedium)
        Text(
            "Sign in to see your approvals and activity. The phone keeps a token from OpenVibe.Network, encrypted with this phone's Keystore.",
            style = MaterialTheme.typography.bodyMedium,
            modifier = Modifier.padding(top = 8.dp, bottom = 12.dp),
        )
        Button(onClick = {
            if (!Session.signIn(context)) Messages.show("No browser answered. Install one and try again.")
        }) { Text("Sign in to OpenVibe") }
    }
}
