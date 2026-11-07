package network.openvibe.link.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import network.openvibe.link.data.ActionItem
import network.openvibe.link.data.Store

/** The Dock's timeline, read-only: everything your agents are doing, from the Action Center. */
@Composable
fun ActivityScreen(preview: List<ActionItem>? = null) {
    LaunchedEffect(Store.signedIn) {
        if (preview == null && Store.signedIn) ActionsRepo.refresh()
    }

    if (preview == null && !Store.signedIn) {
        SignInCard("Activity")
        return
    }

    val items = preview ?: ActionsRepo.activity
    LazyColumn(contentPadding = PaddingValues(20.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        item {
            Text("Activity", style = MaterialTheme.typography.headlineSmall)
            Text(
                "What your agents are running, waiting on, or finished. Read only; answer approvals on their own screen.",
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.padding(top = 4.dp, bottom = 8.dp),
            )
            if (items.isEmpty() && !ActionsRepo.loading) {
                Text("Nothing yet.", style = MaterialTheme.typography.bodyMedium)
            }
            ActionsRepo.error?.let {
                Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
            }
        }
        if (ActionsRepo.loading && ActionsRepo.doc == null) {
            item { CircularProgressIndicator(Modifier.padding(top = 24.dp)) }
        }
        items(items, key = { it.service + it.id + (it.updatedAt ?: "") }) { item ->
            ActivityRow(item)
        }
    }
}
