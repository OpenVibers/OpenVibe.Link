package network.openvibe.link

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity
import androidx.activity.compose.setContent
import network.openvibe.link.data.LinkController
import network.openvibe.link.data.PairLinks
import network.openvibe.link.data.Store
import network.openvibe.link.ui.Incoming
import network.openvibe.link.ui.LinkApp
import network.openvibe.link.ui.LinkTheme

/**
 * The app's one activity. It answers the pairing link (`openvibe-link://pair?code=…`) and the sign-in's return
 * (`openvibe-link://auth?code=…`); both arrive as intents, and the Compose tree reads them through [Incoming].
 */
class MainActivity : FragmentActivity() {
    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) { }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Store.init(applicationContext)
        read(intent)
        // Without "Stay connected" the link lives only while the app is on screen (walked back from in onStop).
        if (Store.paired != null && !Store.stayConnected) LinkController.start()
        setContent { LinkTheme { LinkApp() } }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    override fun onStop() {
        if (!Store.stayConnected) LinkController.stop()
        super.onStop()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        read(intent)
    }

    private fun read(intent: Intent?) {
        val data = intent?.data ?: return
        when (data.host) {
            PairLinks.PAIR_HOST -> {
                val link = PairLinks.parse(data.toString())
                Incoming.pairCode = link?.code ?: data.getQueryParameter("code")
            }
            PairLinks.AUTH_HOST -> Incoming.authUri = data.toString()
        }
    }
}
