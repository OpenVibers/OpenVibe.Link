package network.openvibe.link.data

import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.browser.customtabs.CustomTabsIntent
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.FormBody
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/**
 * The person's own OpenVibe.Network session, for the Approvals and Activity screens and for sending files to their
 * Download drive. It is a public OAuth client (`link`): authorization code + PKCE S256, no secret in the APK, and the
 * tokens are encrypted with the Android Keystore (Store).
 *
 * The code comes back on `https://openvibe.network/link/auth`, an Android App Link claimed for this package by
 * Network's /.well-known/assetlinks.json: a custom scheme could be answered by any app, this one only by us. The
 * verifier never leaves the phone except in the exchange it belongs to, and no token is ever logged.
 */
object Session {
    private const val CLIENT_ID = "link"
    const val REDIRECT_URI = "https://openvibe.network/link/auth"
    private val AUTHORIZE: String get() = "${Api.NETWORK}/oauth/authorize"
    private val TOKEN: String get() = "${Api.NETWORK}/oauth/token"
    private const val SCOPE = "profile theme"

    private val client = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .build()

    private var verifier: String? = null
    private var state: String? = null

    /** The verifier this app is waiting to redeem; null when no sign-in is in flight. */
    val pendingVerifier: String? get() = verifier

    /** Open the account chooser in a Custom Tab. Returns false when no browser answered. */
    fun signIn(context: Context): Boolean {
        val v = Pkce.verifier()
        val s = Pkce.state()
        verifier = v
        state = s
        val url = Uri.parse(AUTHORIZE).buildUpon()
            .appendQueryParameter("response_type", "code")
            .appendQueryParameter("client_id", CLIENT_ID)
            .appendQueryParameter("redirect_uri", REDIRECT_URI)
            .appendQueryParameter("scope", SCOPE)
            .appendQueryParameter("state", s)
            .appendQueryParameter("code_challenge", Pkce.challenge(v))
            .appendQueryParameter("code_challenge_method", "S256")
            .build()
        return try {
            CustomTabsIntent.Builder().setShowTitle(true).build().launchUrl(context, url)
            true
        } catch (e: Exception) {
            try {
                context.startActivity(Intent(Intent.ACTION_VIEW, url).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                true
            } catch (e2: Exception) {
                false
            }
        }
    }

    /** Redeem the code the browser returned. Throws with what went wrong; the caller shows it. */
    suspend fun complete(uri: Uri): Unit = withContext(Dispatchers.IO) {
        val error = uri.getQueryParameter("error")
        if (error != null) throw IllegalStateException(
            if (error == "access_denied") "Sign-in was cancelled." else "Sign-in failed: $error",
        )
        val code = uri.getQueryParameter("code") ?: throw IllegalStateException("Sign-in returned no code.")
        val returned = uri.getQueryParameter("state")
        val expected = state
        val v = verifier
        if (expected == null || returned != expected) throw IllegalStateException("Sign-in returned for another request.")
        if (!Pkce.isVerifier(v)) throw IllegalStateException("Sign-in lost its verifier; try again.")

        val body = FormBody.Builder()
            .add("grant_type", "authorization_code")
            .add("client_id", CLIENT_ID)
            .add("redirect_uri", REDIRECT_URI)
            .add("code", code)
            .add("code_verifier", v!!)
            .build()
        val request = Request.Builder().url(TOKEN).post(body).header("Accept", "application/json").build()
        client.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            if (!response.isSuccessful) throw IllegalStateException(problem(text, response.code))
            val json = JSONObject(text)
            val access = json.optString("access_token")
            if (access.isEmpty()) throw IllegalStateException("Sign-in returned no token.")
            val expiresIn = json.optLong("expires_in", 3600L)
            Store.saveTokens(access, json.optString("refresh_token").takeIf { it.isNotEmpty() }, System.currentTimeMillis() + expiresIn * 1000)
        }
        verifier = null
        state = null
    }

    /** A usable access token, refreshed when it is close to its expiry. Null means sign in again. */
    suspend fun token(): String? = withContext(Dispatchers.IO) {
        val access = Store.accessToken()
        val expires = Store.expiresAt()
        if (access != null && System.currentTimeMillis() < expires - 60_000) return@withContext access
        val refresh = Store.refreshToken() ?: return@withContext access.takeIf { System.currentTimeMillis() < expires }
        val body = FormBody.Builder()
            .add("grant_type", "refresh_token")
            .add("client_id", CLIENT_ID)
            .add("refresh_token", refresh)
            .build()
        val request = Request.Builder().url(TOKEN).post(body).header("Accept", "application/json").build()
        try {
            client.newCall(request).execute().use { response ->
                val text = response.body?.string().orEmpty()
                if (!response.isSuccessful) {
                    if (response.code == 400 || response.code == 401) Store.clearTokens()
                    return@withContext null
                }
                val json = JSONObject(text)
                val next = json.optString("access_token").takeIf { it.isNotEmpty() } ?: return@withContext null
                val expiresIn = json.optLong("expires_in", 3600L)
                Store.saveTokens(next, json.optString("refresh_token").takeIf { it.isNotEmpty() } ?: refresh, System.currentTimeMillis() + expiresIn * 1000)
                next
            }
        } catch (e: Exception) {
            null
        }
    }

    fun signOut() {
        verifier = null
        state = null
        Store.clearTokens()
    }

    /** A token is held (it may still need a refresh; [token] does that). */
    fun hasSession(): Boolean = Store.accessToken() != null

    private fun problem(text: String, status: Int): String {
        val detail = try {
            JSONObject(text).optString("error_description").takeIf { it.isNotEmpty() }
        } catch (e: Exception) {
            null
        }
        return detail ?: "Sign-in was refused ($status)."
    }
}
