package network.openvibe.link

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import network.openvibe.link.data.Session
import network.openvibe.link.data.Store
import network.openvibe.link.service.ShareUploadService
import network.openvibe.link.share.ConfirmShare
import network.openvibe.link.share.ShareIntents
import network.openvibe.link.share.ShareRequest
import network.openvibe.link.share.ShareUploader
import network.openvibe.link.ui.LinkTheme

/**
 * "Send to OpenVibe" from any app's share sheet. Nothing is sent on the intent alone: the sheet asks first, and only
 * "Send" starts the upload (the drive's own, the same one the web page uses) in a foreground service, with progress
 * in a notification. Cancel closes the sheet and the source app comes back with the file untouched.
 */
class ShareActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Store.init(applicationContext)

        val intent = intent
        val uris = mutableListOf<String>()
        val clip = intent.clipData
        if (clip != null && clip.itemCount > 0) {
            for (i in 0 until clip.itemCount) clip.getItemAt(i).uri?.let { uris.add(it.toString()) }
        } else {
            @Suppress("DEPRECATION")
            (intent.getParcelableExtra<Uri>(Intent.EXTRA_STREAM))?.let { uris.add(it.toString()) }
        }
        val request = ShareIntents.parse(
            intent.action,
            intent.type,
            uris,
            intent.getStringExtra(Intent.EXTRA_TEXT),
        )
        if (request == null) {
            Toast.makeText(this, "There is nothing to send.", Toast.LENGTH_SHORT).show()
            finish()
            return
        }
        if (Store.paired == null) {
            Toast.makeText(this, "Pair this phone with OpenVibe first.", Toast.LENGTH_LONG).show()
            startActivity(Intent(this, MainActivity::class.java))
            finish()
            return
        }
        if (!Session.hasSession()) {
            Toast.makeText(this, "Sign in to OpenVibe to send files.", Toast.LENGTH_LONG).show()
            startActivity(Intent(this, MainActivity::class.java))
            finish()
            return
        }

        setContent {
            LinkTheme {
                ConfirmShare(
                    request = request,
                    onSend = { send(request); finish() },
                    onCancel = { finish() },
                )
            }
        }
    }

    private fun send(request: ShareRequest) {
        val files = request.uris.toMutableList()
        val names = mutableListOf<String>()
        if (files.isEmpty() && request.text != null) {
            val (uri, name) = ShareUploader.textFile(this, request.text)
            files.add(uri.toString())
            names.add(name)
        }
        ShareUploadService.start(this, files, names, request.mimeType)
        Toast.makeText(this, if (files.size > 1) "Sending ${files.size} files…" else "Sending to OpenVibe…", Toast.LENGTH_SHORT).show()
    }
}
