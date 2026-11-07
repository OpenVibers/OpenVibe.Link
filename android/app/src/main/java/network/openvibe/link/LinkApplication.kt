package network.openvibe.link

import android.app.Application
import network.openvibe.link.data.Store
import network.openvibe.link.service.ShareUploadService
import network.openvibe.link.service.StayConnectedService

class LinkApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        Store.init(this)
        StayConnectedService.ensureChannel(this)
        ShareUploadService.ensureChannel(this)
        if (Store.paired != null && Store.stayConnected) {
            try {
                StayConnectedService.start(this)
            } catch (e: Exception) {
                // Android may refuse a background start; the switch starts it the next time the app is opened.
            }
        }
    }
}
