package network.openvibe.link.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import network.openvibe.link.MainActivity
import network.openvibe.link.R
import network.openvibe.link.data.LinkController
import network.openvibe.link.data.Store

/**
 * "Stay connected": the phone's one outbound connection to OpenVibe.Bot, held by an opt-in foreground service with a
 * notification the person can always see and stop (the same rule as the plan's "always visible", without any phone
 * automation). Nothing else runs here: no camera, no screen capture, no accessibility.
 */
class StayConnectedService : Service() {
    private val main = Handler(Looper.getMainLooper())
    private var tick: Runnable? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        ensureChannel(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            LinkController.stop()
            Store.keepConnected(false)
            stopSelf()
            return START_NOT_STICKY
        }
        Store.keepConnected(true)
        startForegroundCompat(this, NOTIFICATION_ID, notification("Connecting to OpenVibe…"))
        LinkController.start()
        watch()
        return START_STICKY
    }

    override fun onDestroy() {
        tick?.let { main.removeCallbacks(it) }
        tick = null
        if (!Store.stayConnected) LinkController.stop()
        super.onDestroy()
    }

    private fun watch() {
        tick?.let { main.removeCallbacks(it) }
        val runnable = object : Runnable {
            override fun run() {
                val text = when {
                    LinkController.connected -> "Connected as ${Store.deviceLabel.ifBlank { "this phone" }}"
                    LinkController.state == "revoked" -> "This phone was unpaired: pair it again"
                    else -> "Offline. Reconnecting…"
                }
                val manager = getSystemService(NotificationManager::class.java)
                manager?.notify(NOTIFICATION_ID, notification(text))
                if (LinkController.state != "revoked") main.postDelayed(this, 5_000)
            }
        }
        tick = runnable
        main.post(runnable)
    }

    private fun notification(text: String): Notification {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, StayConnectedService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_notify)
            .setContentTitle("OpenVibe Link")
            .setContentText(text)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(open)
            .addAction(0, "Stop", stop)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()
    }

    companion object {
        const val ACTION_START = "network.openvibe.link.STAY_START"
        const val ACTION_STOP = "network.openvibe.link.STAY_STOP"
        const val CHANNEL = "stay_connected"
        private const val NOTIFICATION_ID = 1001

        fun ensureChannel(context: Context) {
            val manager = context.getSystemService(NotificationManager::class.java) ?: return
            if (manager.getNotificationChannel(CHANNEL) == null) {
                manager.createNotificationChannel(
                    NotificationChannel(CHANNEL, context.getString(R.string.channel_stay_connected), NotificationManager.IMPORTANCE_LOW).apply {
                        description = "The phone's connection to OpenVibe"
                        setShowBadge(false)
                    },
                )
            }
        }

        fun start(context: Context) {
            ensureChannel(context)
            val intent = Intent(context, StayConnectedService::class.java).setAction(ACTION_START)
            ContextCompat.startForegroundService(context, intent)
        }

        fun stop(context: Context) {
            context.startService(Intent(context, StayConnectedService::class.java).setAction(ACTION_STOP))
            context.stopService(Intent(context, StayConnectedService::class.java))
        }

        fun startForegroundCompat(service: Service, id: Int, notification: Notification) {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                service.startForeground(id, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
            } else {
                service.startForeground(id, notification)
            }
        }
    }
}
