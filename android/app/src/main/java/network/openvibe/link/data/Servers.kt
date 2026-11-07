package network.openvibe.link.data

import android.net.Uri
import network.openvibe.link.BuildConfig

/**
 * Where this build of the app signs in and pairs. It is a build constant, never the QR's or the person's choice: a code
 * and a redirect belong to one server, and a link naming another is refused (PairLinks).
 *
 * A release build always carries https://openvibe.network. A debug build may be pointed elsewhere when it is built
 * (`./gradlew :app:assembleDebug -Pnetwork.url=https://…`); that build says so in a banner on every screen, so a
 * screenshot or a bug report can never be mistaken for production.
 */
object Servers {
    const val OFFICIAL = "https://openvibe.network"

    /** The server this build talks to, with no trailing slash. */
    val network: String get() = BuildConfig.NETWORK_URL.trimEnd('/')

    /** True in a debug build that was pointed at another server: what the banner shows. */
    val isDebugOverride: Boolean get() = BuildConfig.DEBUG_SERVER && network != OFFICIAL

    /** The host, for a sentence ("… points at evil.test"). */
    val host: String get() = Uri.parse(network).host ?: network

    /** Two server URLs are the same when they differ only by case, slashes or an empty path. */
    fun same(a: String?, b: String?): Boolean =
        normalize(a) != null && normalize(a) == normalize(b)

    fun normalize(url: String?): String? {
        val v = url?.trim()?.trimEnd('/') ?: return null
        return if (v.startsWith("https://", ignoreCase = true)) v.lowercase() else null
    }
}
