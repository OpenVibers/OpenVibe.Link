package network.openvibe.link

import network.openvibe.link.data.PairLinks
import network.openvibe.link.data.Pkce
import network.openvibe.link.data.Servers
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class PairLinksTest {
    private val ours = "https://openvibe.network"

    @Test
    fun `the QR link carries the code and the pairing id`() {
        val link = PairLinks.parse("openvibe-link://pair?code=ABCD-1234&pairing=pair_01JD2C3D4E5F6G7H8J9K0MNPQR", ours)!!
        assertEquals("ABCD-1234", link.code)
        assertEquals("pair_01JD2C3D4E5F6G7H8J9K0MNPQR", link.pairing)
        assertNull(link.refusedServer)
        assertEquals(ours, link.network)
        assertNull(PairLinks.parse("openvibe-link://pair?code=ABCD-1234&pairing=nonsense", ours)!!.pairing)
    }

    @Test
    fun `a link with no server is ours`() {
        assertNull(PairLinks.parse("openvibe-link://pair?code=ABCD-1234", ours)!!.refusedServer)
        assertEquals(ours, PairLinks.parse("openvibe-link://pair?code=ABCD-1234", ours)!!.network)
    }

    @Test
    fun `a link that names another server is refused whole, with no code`() {
        val link = PairLinks.parse("openvibe-link://pair?code=ABCD-1234&network=https%3A%2F%2Fevil.test", ours)!!
        assertEquals("evil.test", link.refusedServer)
        assertNull("a code from another server is never redeemed", link.code)
        assertNull(link.pairing)
        // A plain http URL is not a server this app can talk to either.
        assertEquals("http://evil.test", PairLinks.parse("openvibe-link://pair?code=ABCD-1234&network=http%3A%2F%2Fevil.test", ours)!!.refusedServer)
        // Our own server, written differently, is still ours.
        assertNull(PairLinks.parse("openvibe-link://pair?code=ABCD-1234&network=https%3A%2F%2FOPENVIBE.network%2F", ours)!!.refusedServer)
    }

    @Test
    fun `another scheme is not a pairing link`() {
        assertNull(PairLinks.parse("https://openvibe.bot/link/phone?code=ABCD-1234", ours))
        assertNull(PairLinks.parse("openvibe-link://auth?code=ABCD-1234", ours))
        assertNull(PairLinks.parse(null, ours))
    }

    @Test
    fun `a scan of a bare code or a page link is read`() {
        // The code is passed to the Go core as it was scanned; it normalizes the Crockford form.
        assertEquals("abcd 1234", PairLinks.codeFromScan("abcd 1234", ours)!!.code)
        assertEquals("ABCD-1234", PairLinks.codeFromScan("https://openvibe.bot/link/phone?code=ABCD-1234", ours)!!.code)
        assertEquals("evil.test", PairLinks.codeFromScan("https://openvibe.bot/link/phone?code=ABCD-1234&network=https%3A%2F%2Fevil.test", ours)!!.refusedServer)
        assertNull(PairLinks.codeFromScan("https://example.test/hello", ours))
    }
}

class ServersTest {
    @Test
    fun `a server compares by scheme, host and path, not spelling`() {
        assertTrue(Servers.same("https://openvibe.network", "https://openvibe.network/"))
        assertTrue(Servers.same("https://OpenVibe.Network", "https://openvibe.network"))
        assertFalse(Servers.same("https://evil.test", "https://openvibe.network"))
        assertFalse("https only", Servers.same("http://openvibe.network", "https://openvibe.network"))
        assertNull(Servers.normalize("openvibe.network"))
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
