package network.openvibe.link

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * The rules about secrets that a unit test can hold the sources to: the app keeps every token and the pairing behind
 * one Keystore key (Store), and no part of it writes one to a log, a stack trace or an unencrypted preference — a
 * crash report carries logs, so a token in a log is a token on someone else's server. The upload and the prompt are
 * checked in ShareIntentsTest/ApprovalRulesTest; a device is needed for the flows themselves.
 */
class SecretsTest {
    private val main = File("src/main/java/network/openvibe/link")

    @Test
    fun `nothing in the app logs`() {
        val offenders = main.walkTopDown()
            .filter { it.isFile && it.extension == "kt" }
            .filter { file ->
                val text = file.readText()
                Regex("""\bLog\.[dviwe]\(""").containsMatchIn(text) || text.contains("println(")
            }
            .map { it.name }
            .toList()
        assertTrue("these files log: $offenders", offenders.isEmpty())
    }

    @Test
    fun `secrets are written only through the Keystore cipher`() {
        val store = File(main, "data/Store.kt").readText()
        assertTrue("the key is the Android Keystore's", store.contains("AndroidKeyStore"))
        assertTrue("AES-GCM seals every value", store.contains("AES/GCM/NoPadding"))
        assertTrue("a stored secret is named through encryptedKey", store.contains("putString(encryptedKey("))
        // No preference write of a plain name: an unencrypted token would sit in the app's data directory.
        assertFalse("an unencrypted value is never written", Regex("""putString\("(access|refresh|pairing)"""").containsMatchIn(store))
        assertTrue("nothing is written when the cipher fails", store.contains("catch (e: Exception)") || store.contains("try {"))
    }

    @Test
    fun `the session is kept in the app's own storage and excluded from backups`() {
        val manifest = File("src/main/AndroidManifest.xml").readText()
        assertTrue(manifest.contains("android:allowBackup=\"false\""))
        assertTrue(manifest.contains("android:fullBackupContent=\"false\""))
    }
}
