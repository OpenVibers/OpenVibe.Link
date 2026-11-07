package network.openvibe.link.ui

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
    var network by rememberSaveable { mutableStateOf(PairLinks.DEFAULT_NETWORK) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(initialCode) {
        if (!initialCode.isNullOrBlank()) code = initialCode
    }

    val scanner = rememberLauncherForActivityResult(ScanContract()) { result ->
        val link = PairLinks.codeFromScan(result.contents)
        when {
            link?.code != null -> {
                code = link.code
                link.network?.let { network = it }
            }
            result.contents != null -> Messages.show("That QR is not an OpenVibe pairing link.")
        }
    }
    val askCamera = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) launchScanner(scanner) else Messages.show("The camera is how the QR is read; you can type the code instead.")
    }

    Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(24.dp)) {
        Text("Link this phone", style = MaterialTheme.typography.headlineSmall)
        Text(
            "Open openvibe.bot/link/phone on a computer signed in as you, then type the code it shows or scan its QR. " +
                "This phone becomes one of your devices: approvals in your pocket, and Send to OpenVibe from any app.",
            style = MaterialTheme.typography.bodyMedium,
            modifier = Modifier.padding(top = 12.dp, bottom = 20.dp),
        )

        OutlinedTextField(
            value = code,
            onValueChange = { code = it.uppercase() },
            label = { Text("Pairing code") },
            placeholder = { Text("ABCD-1234") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )

        OutlinedTextField(
            value = network,
            onValueChange = { network = it },
            label = { Text("OpenVibe.Network") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth().padding(top = 12.dp),
        )

        error?.let {
            Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(top = 12.dp))
        }

        Button(
            onClick = {
                if (busy) return@Button
                busy = true
                error = null
                scope.launch {
                    try {
                        val name = "Link · ${Build.MODEL ?: "phone"}"
                        val pairing = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
                            mobile.Mobile.pair(network.trim(), Api.BOT, code, name)
                        }
                        Store.savePairing(pairing, name)
                        Messages.show("This phone is linked.")
                    } catch (e: Exception) {
                        error = e.message ?: "Pairing failed."
                    } finally {
                        busy = false
                    }
                }
            },
            enabled = !busy && code.isNotBlank(),
            modifier = Modifier.fillMaxWidth().padding(top = 20.dp),
        ) {
            if (busy) {
                CircularProgressIndicator(Modifier.padding(end = 12.dp), strokeWidth = 2.dp)
            }
            Text(if (busy) "Pairing…" else "Pair this phone")
        }

        OutlinedButton(
            onClick = {
                if (ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
                    launchScanner(scanner)
                } else {
                    askCamera.launch(Manifest.permission.CAMERA)
                }
            },
            modifier = Modifier.fillMaxWidth().padding(top = 12.dp),
        ) { Text("Scan the QR instead") }

        Text(
            "The code works once and lasts 15 minutes. Nothing on this phone is backed up: the pairing is encrypted with a key this phone's Keystore holds.",
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
