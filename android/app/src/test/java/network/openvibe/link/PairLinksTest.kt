package network.openvibe.link

import network.openvibe.link.data.PairLinks
import network.openvibe.link.data.Pkce
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class PairLinksTest {
    @Test
    fun `the QR link carries the code and the network`() {
        val link = PairLinks.parse("openvibe-link://pair?code=ABCD-1234&network=https%3A%2F%2Fopenvibe.network")!!
        assertEquals("ABCD-1234", link.code)
        assertEquals("https://openvibe.network", link.network)
    }

    @Test
    fun `a link with no network uses ours`() {
        assertEquals(PairLinks.DEFAULT_NETWORK, PairLinks.parse("openvibe-link://pair?code=ABCD-1234")!!.network)
    }

    @Test
    fun `a plain http network is not taken from a QR`() {
        assertEquals(PairLinks.DEFAULT_NETWORK, PairLinks.parse("openvibe-link://pair?code=ABCD-1234&network=http://evil.test")!!.network)
    }

    @Test
    fun `another scheme is not a pairing link`() {
        assertNull(PairLinks.parse("https://openvibe.bot/link/phone?code=ABCD-1234"))
        assertNull(PairLinks.parse("openvibe-link://auth?code=ABCD-1234"))
        assertNull(PairLinks.parse(null))
    }

    @Test
    fun `a scan of a bare code or a page link is read`() {
        // The code is passed to the Go core as it was scanned; it normalizes the Crockford form.
        assertEquals("abcd 1234", PairLinks.codeFromScan("abcd 1234")!!.code)
        assertEquals("ABCD-1234", PairLinks.codeFromScan("https://openvibe.bot/link/phone?code=ABCD-1234")!!.code)
        assertNull(PairLinks.codeFromScan("https://example.test/hello"))
    }
}

class PkceTest {
    @Test
    fun `the verifier and the challenge are the RFC 7636 pair`() {
        val verifier = Pkce.verifier()
        assertTrue(Pkce.isVerifier(verifier))
        assertEquals(43, verifier.length)
        // RFC 7636 appendix B: SHA-256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") in base64url.
        assertEquals("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", Pkce.challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"))
    }

    @Test
    fun `two requests get different verifiers and states`() {
        assertNotEquals(Pkce.verifier(), Pkce.verifier())
        assertNotEquals(Pkce.state(), Pkce.state())
    }

    @Test
    fun `a short verifier is refused`() {
        assertFalse(Pkce.isVerifier("short"))
        assertFalse(Pkce.isVerifier(null))
    }
}
