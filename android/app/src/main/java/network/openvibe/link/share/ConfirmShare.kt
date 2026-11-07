package network.openvibe.link.share

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp

/**
 * The share sheet's question, asked before anything leaves the phone: a share intent is another app's request, not the
 * person's decision. Send starts the upload; Cancel closes the sheet and the source app comes back with the file
 * untouched.
 */
@Composable
fun ConfirmShare(request: ShareRequest, onSend: () -> Unit, onCancel: () -> Unit) {
    Surface(color = MaterialTheme.colorScheme.background) {
        Column(Modifier.fillMaxWidth().padding(24.dp)) {
            Text(request.question, style = MaterialTheme.typography.headlineSmall)
            Text(
                "It goes to your private Download drive, as you. Nothing is re-encoded.",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(top = 8.dp, bottom = 20.dp),
            )
            request.text?.let {
                Text(
                    it.take(140),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(bottom = 20.dp),
                )
            }
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                Button(onClick = onSend) { Text("Send") }
                Spacer(Modifier.width(12.dp))
                TextButton(onClick = onCancel) { Text("Cancel") }
            }
        }
    }
}
