package network.openvibe.link.service

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import network.openvibe.link.MainActivity
import network.openvibe.link.R
import network.openvibe.link.share.ShareUploader
import java.util.concurrent.atomic.AtomicInteger

/**
 * The share sheet's upload, as a foreground service with progress: the person can leave the app while a photo or a
 * file goes to their Download drive, and the notification says where it is.
 */
class ShareUploadService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        ensureChannel(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val uris = intent?.getStringArrayListExtra(EXTRA_URIS).orEmpty()
        val mime = intent?.getStringExtra(EXTRA_MIME).orEmpty().ifBlank { "application/octet-stream" }
        val names = intent?.getStringArrayListExtra(EXTRA_NAMES).orEmpty()
        StayConnectedService.startForegroundCompat(this, NOTIFICATION_ID, progress("Preparing…", 0, 1))
        if (uris.isEmpty()) {
            stopSelf(startId)
            return START_NOT_STICKY
        }
        scope.launch {
            var sent = 0
            var failed = 0
            uris.forEachIndexed { index, raw ->
                val uri = Uri.parse(raw)
                try {
                    val result = ShareUploader.send(this@ShareUploadService, uri, mime) { done, total ->
                        notify(progress("Sending ${index + 1} of ${uris.size} · ${percent(done, total)}", index + 1, uris.size))
                    }
                    sent++
                    notify(progress("Sent ${result.name}", index + 1, uris.size))
                } catch (e: Exception) {
                    failed++
                    notify(progress("Could not send ${names.getOrNull(index) ?: "a file"}: ${e.message ?: "error"}", index + 1, uris.size))
                }
            }
            val title = when {
                failed == 0 -> if (sent == 1) "Sent to OpenVibe" else "$sent files sent to OpenVibe"
                sent == 0 -> "Nothing was sent"
                else -> "$sent sent, $failed failed"
            }
            done(title, failed == 0)
            stopForeground(STOP_FOREGROUND_DETACH)
            stopSelf(startId)
        }
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    private fun progress(text: String, done: Int, total: Int): android.app.Notification {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_notify)
            .setContentTitle("OpenVibe · Download")
            .setContentText(text)
            .setProgress(total, done, false)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(open)
            .setCategory(NotificationCompat.CATEGORY_PROGRESS)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()
    }

    private fun done(text: String, ok: Boolean): android.app.Notification {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_notify)
            .setContentTitle(text)
            .setContentText(if (ok) "In your Download drive on openvibe.download" else "Open the app to try again")
            .setAutoCancel(true)
            .setContentIntent(open)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .build()
    }

    private fun notify(notification: android.app.Notification) {
        getSystemService(NotificationManager::class.java)?.notify(NOTIFICATION_ID, notification)
    }

    private fun percent(done: Long, total: Long): String {
        if (total <= 0) return ""
        return "${(done * 100 / total).coerceIn(0, 100)}%"
    }

    companion object {
        const val CHANNEL = "downloads"
        private const val NOTIFICATION_ID = 1002
        const val EXTRA_URIS = "network.openvibe.link.URIS"
        const val EXTRA_NAMES = "network.openvibe.link.NAMES"
        const val EXTRA_MIME = "network.openvibe.link.MIME"

        private val nextId = AtomicInteger(1)

        fun ensureChannel(context: Context) {
            val manager = context.getSystemService(NotificationManager::class.java) ?: return
            if (manager.getNotificationChannel(CHANNEL) == null) {
                manager.createNotificationChannel(
                    NotificationChannel(CHANNEL, context.getString(R.string.channel_downloads), NotificationManager.IMPORTANCE_LOW).apply {
                        description = "Files you send to your Download drive"
                        setShowBadge(false)
                    },
                )
            }
        }

        fun start(context: Context, uris: List<String>, names: List<String>, mime: String) {
            ensureChannel(context)
            val intent = Intent(context, ShareUploadService::class.java)
                .putStringArrayListExtra(EXTRA_URIS, ArrayList(uris))
                .putStringArrayListExtra(EXTRA_NAMES, ArrayList(names))
                .putExtra(EXTRA_MIME, mime)
            ContextCompat.startForegroundService(context, intent)
        }
    }
}
