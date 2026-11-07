package network.openvibe.link.ui

import androidx.compose.material3.FilledTonalButton
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.QrCodeScanner
import androidx.compose.material3.Icon
import androidx.compose.ui.Alignment
import androidx.compose.ui.text.font.FontWeight
import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.launch
import network.openvibe.link.data.Api
import network.openvibe.link.data.PairLinks
import network.openvibe.link.data.Store

/**
 * The first screen: the code from openvibe.bot/link/phone, typed or scanned. Pairing is the Node's own: the code is
 * redeemed on OpenVibe.Network and the phone is bound on OpenVibe.Bot as a device of kind "phone".
 */
@Composable
fun PairScreen(initialCode: String?) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var code by rememberSaveable { mutableStateOf(initialCode.orEmpty()) }
    var pairing by rememberSaveable { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(initialCode) {
        if (!initialCode.isNullOrBlank()) code = initialCode
    }
    // A link that named another server was refused before it got here (MainActivity, PairLinks): say which, plainly.
    LaunchedEffect(Incoming.pairRefused) {
        val refused = Incoming.pairRefused ?: return@LaunchedEffect
        error = "That link points at $refused, not OpenVibe Network. Only a code from openvibe.bot/link/phone pairs this phone."
        Incoming.pairRefused = null
    }

    val scanner = rememberLauncherForActivityResult(ScanContract()) { result ->
        val link = PairLinks.codeFromScan(result.contents)
        when {
            link?.refusedServer != null -> {
                error = "That QR points at ${link.refusedServer}, not OpenVibe Network."
                Messages.show("That QR points at ${link.refusedServer}, not OpenVibe Network.")
            }
            link?.code != null -> {
                code = link.code
                pairing = link.pairing
            }
            result.contents != null -> Messages.show("That QR is not an OpenVibe pairing link.")
        }
    }
    val askCamera = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) launchScanner(scanner) else Messages.show("The camera is how the QR is read; you can type the code instead.")
    }

    Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(24.dp)) {
        Text("Link this phone", style = MaterialTheme.typography.headlineMedium, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.onBackground)
        Text(
            "Your agents' approvals in your pocket, and Send to OpenVibe from any app.",
            style = MaterialTheme.typography.bodyLarge,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(top = 8.dp, bottom = 20.dp),
        )
        LinkCard {
            PairStep(1, "On your computer, open openvibe.bot/link/phone, signed in as you.")
            PairStep(2, "Scan the QR it shows, or type its code below.")
        }
        Button(
            onClick = {
                if (ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
                    launchScanner(scanner)
                } else {
                    askCamera.launch(Manifest.permission.CAMERA)
                }
            },
            modifier = Modifier.fillMaxWidth().padding(top = 16.dp).heightIn(min = 52.dp),
        ) {
            Icon(Icons.Outlined.QrCodeScanner, contentDescription = null, modifier = Modifier.padding(end = 10.dp))
            Text("Scan the QR", fontWeight = FontWeight.Bold)
        }
        Text(
            "Or type the code",
            style = MaterialTheme.typography.labelLarge,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(top = 24.dp, bottom = 8.dp),
        )

        OutlinedTextField(
            value = code,
            onValueChange = { code = it.uppercase() },
            label = { Text("Pairing code") },
            placeholder = { Text("ABCD-1234") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )

        error?.let {
            Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(top = 12.dp))
        }

        FilledTonalButton(
            onClick = {
                if (busy) return@FilledTonalButton
                busy = true
                error = null
                scope.launch {
                    try {
                        val name = "Link · ${Build.MODEL ?: "phone"}"
                        val creds = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
                            mobile.Mobile.pair(Api.NETWORK, Api.BOT, code, name, pairing ?: "")
                        }
                        Store.savePairing(creds, name)
                        Messages.show("This phone is linked.")
                    } catch (e: Exception) {
                        error = e.message ?: "Pairing failed."
                    } finally {
                        busy = false
                    }
                }
            },
            enabled = !busy && code.isNotBlank(),
            modifier = Modifier.fillMaxWidth().padding(top = 12.dp).heightIn(min = 48.dp),
        ) {
            Text(if (busy) "Pairing…" else "Pair this phone")
        }


        Text(
            "A code works once and lasts 15 minutes. Nothing on this phone is backed up: the pairing is encrypted with a key this phone's Keystore holds.",
            style = MaterialTheme.typography.bodySmall,
            modifier = Modifier.padding(top = 24.dp),
        )
    }
}

private fun launchScanner(scanner: androidx.activity.result.ActivityResultLauncher<ScanOptions>) {
    scanner.launch(
        ScanOptions()
            .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            .setPrompt("Scan the QR on openvibe.bot/link/phone")
            .setBeepEnabled(false)
            .setOrientationLocked(false),
    )
}

/** One numbered step of pairing. */
@Composable
private fun PairStep(n: Int, text: String) {
    Row(verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Box(
            Modifier.size(26.dp).background(MaterialTheme.colorScheme.primary.copy(alpha = 0.16f), CircleShape),
            contentAlignment = Alignment.Center,
        ) { Text("$n", color = MaterialTheme.colorScheme.primary, fontWeight = FontWeight.Bold, style = MaterialTheme.typography.labelLarge) }
        Text(text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurface, modifier = Modifier.padding(top = 3.dp))
    }
}
