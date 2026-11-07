package network.openvibe.link.ui

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import network.openvibe.link.data.ActionItem
import network.openvibe.link.data.ActionsDoc
import network.openvibe.link.data.Api
import network.openvibe.link.data.Session
import network.openvibe.link.data.Store

/** Intents the activity received, for the Compose tree to read (a deep link can arrive while it is already drawn). */
object Incoming {
    var pairCode: String? by mutableStateOf(null)
    /** The server a refused pair link named, so the pair screen can say it instead of pairing with it. */
    var pairRefused: String? by mutableStateOf(null)
    var authUri: String? by mutableStateOf(null)
}

/** One short line for the person: a toast, then cleared. */
object Messages {
    var text: String? by mutableStateOf(null)

    fun show(message: String?) {
        if (!message.isNullOrBlank()) text = message
    }
}

/**
 * The person's Action Center, the same answer the Dock's activity tab and the bell read. It is cached in memory only
 * for the life of the process; every screen that needs it asks for a refresh.
 */
object ActionsRepo {
    var doc: ActionsDoc? by mutableStateOf(null)
        private set
    var loading: Boolean by mutableStateOf(false)
        private set
    var error: String? by mutableStateOf(null)
        private set

    val approvals: List<ActionItem>
        get() = doc?.buckets?.values?.flatten()?.filter { it.isApproval && it.isPending }?.sortedByDescending { it.createdAt }.orEmpty()

    val activity: List<ActionItem>
        get() = doc?.buckets?.values?.flatten()?.sortedByDescending { it.updatedAt ?: it.createdAt }.orEmpty()

    suspend fun refresh() {
        if (!Store.signedIn) return
        if (!Session.hasSession()) return
        loading = true
        error = null
        try {
            doc = Api.actions()
        } catch (e: Exception) {
            error = e.message ?: "Could not read your activity."
        } finally {
            loading = false
        }
    }

    suspend fun decide(item: ActionItem, approve: Boolean) {
        Api.decide(item, approve)
        refresh()
    }

    fun clear() {
        doc = null
        error = null
    }
}
