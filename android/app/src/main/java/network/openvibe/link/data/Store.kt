package network.openvibe.link.data

import android.content.Context
import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Everything secret the app keeps, encrypted with a key the Android Keystore makes and never exports: the pairing's
 * node credential, the session's access and refresh tokens. Losing the key (a cleared Keystore, a restore onto
 * another phone) loses the secrets: the person pairs again, which is the right answer. Nothing secret is ever written
 * to a log or to a crash report, and this app's data is excluded from Android backups.
 */
object Store {
    private const val PREFS = "openvibe-link"
    private const val KEY_ALIAS = "openvibe-link-secrets"
    private const val TRANSFORMATION = "AES/GCM/NoPadding"
    private const val GCM_TAG_BITS = 128

    private lateinit var prefs: SharedPreferences

    /** The pairing JSON the Go core reads; null when this phone is not paired. Compose observes it. */
    var paired: String? by mutableStateOf(null)
        private set

    /** True when a Network session (the person's own token) is held. */
    var signedIn: Boolean by mutableStateOf(false)
        private set

    /** The "Stay connected" switch, remembered across launches. */
    var stayConnected: Boolean by mutableStateOf(false)
        private set

    var deviceLabel: String by mutableStateOf("")
        private set

    fun init(context: Context) {
        prefs = context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        stayConnected = prefs.getBoolean("stay_connected", false)
        deviceLabel = prefs.getString("device_label", "").orEmpty()
        paired = read("pairing")
        signedIn = !read("access").isNullOrEmpty()
    }

    @Synchronized
    fun savePairing(json: String, label: String) {
        write("pairing", json)
        deviceLabel = label
        prefs.edit().putString("device_label", label).apply()
        paired = json
    }

    @Synchronized
    fun forgetPairing() {
        prefs.edit().remove(encryptedKey("pairing")).remove("device_label").apply()
        paired = null
        deviceLabel = ""
    }

    @Synchronized
    fun saveTokens(access: String, refresh: String?, expiresAt: Long) {
        write("access", access)
        refresh?.let { write("refresh", it) }
        prefs.edit().putLong("expires_at", expiresAt).apply()
        signedIn = true
    }

    @Synchronized
    fun clearTokens() {
        prefs.edit().remove(encryptedKey("access")).remove(encryptedKey("refresh")).remove("expires_at").apply()
        signedIn = false
    }

    fun accessToken(): String? = read("access")

    fun refreshToken(): String? = read("refresh")

    fun expiresAt(): Long = prefs.getLong("expires_at", 0L)

    fun keepConnected(on: Boolean) {
        stayConnected = on
        prefs.edit().putBoolean("stay_connected", on).apply()
    }

    private fun encryptedKey(name: String) = "enc_$name"

    private fun write(name: String, value: String) {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val sealed = cipher.iv + cipher.doFinal(value.toByteArray(Charsets.UTF_8))
        prefs.edit().putString(encryptedKey(name), Base64.encodeToString(sealed, Base64.NO_WRAP)).apply()
    }

    private fun read(name: String): String? {
        val stored = prefs.getString(encryptedKey(name), null) ?: return null
        return try {
            val sealed = Base64.decode(stored, Base64.NO_WRAP)
            val iv = sealed.copyOfRange(0, 12)
            val body = sealed.copyOfRange(12, sealed.size)
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(GCM_TAG_BITS, iv))
            String(cipher.doFinal(body), Charsets.UTF_8)
        } catch (e: Exception) {
            // A restored backup or a cleared Keystore: the secret is gone, and the person pairs again.
            null
        }
    }

    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getEntry(KEY_ALIAS, null) as? KeyStore.SecretKeyEntry)?.let { return it.secretKey }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        val spec = KeyGenParameterSpec.Builder(
            KEY_ALIAS,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            .build()
        generator.init(spec)
        return generator.generateKey()
    }
}
