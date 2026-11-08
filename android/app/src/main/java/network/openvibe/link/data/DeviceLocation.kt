package network.openvibe.link.data

import android.content.Context
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.Looper
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeout
import kotlin.coroutines.resume

/** One location reading after the person's tap and Android's permission prompt. No listener stays installed. */
object DeviceLocation {
    suspend fun once(context: Context, precise: Boolean): Location = withTimeout(15_000) {
        val manager = context.getSystemService(LocationManager::class.java)
            ?: throw ApiException("Location is unavailable on this phone.")
        val provider = if (precise) LocationManager.GPS_PROVIDER else LocationManager.NETWORK_PROVIDER
        if (!manager.isProviderEnabled(provider)) throw ApiException("${if (precise) "Precise" else "Approximate"} location is off on this phone.")
        suspendCancellableCoroutine { continuation ->
            val listener = object : LocationListener {
                override fun onLocationChanged(location: Location) {
                    manager.removeUpdates(this)
                    if (continuation.isActive) continuation.resume(location)
                }
            }
            continuation.invokeOnCancellation { manager.removeUpdates(listener) }
            try {
                manager.requestSingleUpdate(provider, listener, Looper.getMainLooper())
            } catch (e: SecurityException) {
                manager.removeUpdates(listener)
                continuation.cancel(ApiException("Location permission was not granted."))
            }
        }
    }
}
