package network.openvibe.link.data

import android.os.Handler
import android.os.Looper
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue

/**
 * The one Go link this app holds, and the state the screens draw. `mobile.Link` is the AAR the Go core is bound into
 * (mobile/mobile.go): the same pairing and reconnecting WebSocket the Node runs. Kotlin only starts, stops and reads it.
 */
object LinkController {
    /** "stopped", "connected", "offline" or "revoked". */
    var state: String by mutableStateOf("stopped")
        private set
    var detail: String? by mutableStateOf(null)
        private set
    var connected: Boolean by mutableStateOf(false)
        private set

    private val main = Handler(Looper.getMainLooper())
    private var link: mobile.Link? = null

    @Synchronized
    fun start() {
        if (link != null) return
        val pairing = Store.paired ?: return
        val started = try {
            mobile.Mobile.newLink(pairing)
        } catch (e: Exception) {
            publish("revoked", "This phone's pairing could not be read. Pair it again.")
            return
        }
        link = started
        publish("offline", "Connecting…")
        try {
            started.start(object : mobile.Listener {
                override fun onState(event: String) = onGoState(event)
            })
        } catch (e: Exception) {
            publish("offline", "Could not start the connection.")
            link = null
        }
    }

    @Synchronized
    fun stop() {
        val current = link ?: return
        link = null
        try {
            current.stop()
        } catch (e: Exception) {
            // The Go side is already gone; nothing to close.
        }
        publish("stopped", null)
    }

    /** What "This phone" shows, from the Go core: the device, the account, the kind and whether it is bound. */
    fun info(): String? {
        val pairing = Store.paired ?: return null
        return try {
            mobile.Mobile.info(pairing)
        } catch (e: Exception) {
            null
        }
    }

    private fun onGoState(event: String) {
        val state = eventState(event)
        val detail = eventDetail(event)
        main.post {
            when (state) {
                "connected" -> {
                    connected = true
                    this.state = "connected"
                    this.detail = null
                }
                "offline" -> {
                    connected = false
                    this.state = "offline"
                    this.detail = detail
                }
                "stopped" -> {
                    connected = false
                    this.state = "stopped"
                    this.detail = null
                }
                "revoked" -> {
                    connected = false
                    this.state = "revoked"
                    this.detail = detail
                }
            }
        }
    }

    private fun publish(next: String, message: String?) {
        main.post {
            state = next
            detail = message
            connected = next == "connected"
        }
    }

    private fun eventState(event: String): String = try {
        org.json.JSONObject(event).optString("state", "offline")
    } catch (e: Exception) {
        "offline"
    }

    private fun eventDetail(event: String): String? = try {
        org.json.JSONObject(event).optString("detail").takeIf { it.isNotEmpty() }
    } catch (e: Exception) {
        null
    }
}
