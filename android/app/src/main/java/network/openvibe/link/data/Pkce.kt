package network.openvibe.link.data

import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64

/**
 * PKCE (RFC 7636, S256) for the sign-in. The phone is a public OAuth client: it holds no secret, and the verifier it
 * generates here is the only proof that the code it redeems is its own. Kept away from Android so it has tests.
 */
object Pkce {
    private val random = SecureRandom()

    /** 43 characters of base64url without padding, from 32 random bytes. */
    fun verifier(): String {
        val bytes = ByteArray(32)
        random.nextBytes(bytes)
        return Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)
    }

    fun challenge(verifier: String): String {
        val digest = MessageDigest.getInstance("SHA-256").digest(verifier.toByteArray(Charsets.US_ASCII))
        return Base64.getUrlEncoder().withoutPadding().encodeToString(digest)
    }

    /** A random `state`, so a code that comes back from another request is refused. */
    fun state(): String {
        val bytes = ByteArray(16)
        random.nextBytes(bytes)
        return Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)
    }

    /** The shape the server accepts (PKCE_RE): 43–128 unreserved characters. */
    fun isVerifier(value: String?): Boolean =
        value != null && value.length in 43..128 && value.all { it.isLetterOrDigit() || it == '-' || it == '_' || it == '.' || it == '~' }
}
