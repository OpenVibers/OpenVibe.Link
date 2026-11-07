package network.openvibe.link

import network.openvibe.link.share.ShareIntents
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ShareIntentsTest {
    @Test
    fun `one file`() {
        val share = ShareIntents.parse(ShareIntents.ACTION_SEND, "image/jpeg", listOf("content://media/1"), null)!!
        assertEquals("image/jpeg", share.mimeType)
        assertEquals(listOf("content://media/1"), share.uris)
        assertFalse(share.isMultiple)
        assertTrue(share.hasFiles)
        assertNull(share.text)
    }

    @Test
    fun `several files keep their order and drop repeats`() {
        val share = ShareIntents.parse(
            ShareIntents.ACTION_SEND_MULTIPLE,
            "image/*",
            listOf("content://media/1", "content://media/2", "content://media/1"),
            null,
        )!!
        assertEquals(listOf("content://media/1", "content://media/2"), share.uris)
        assertTrue(share.isMultiple)
    }

    @Test
    fun `a single send takes the first stream only`() {
        val share = ShareIntents.parse(ShareIntents.ACTION_SEND, "application/pdf", listOf("content://a", "content://b"), null)!!
        assertEquals(listOf("content://a"), share.uris)
        assertFalse(share.isMultiple)
    }

    @Test
    fun `text on its own is a text share`() {
        val share = ShareIntents.parse(ShareIntents.ACTION_SEND, "text/plain", emptyList(), "  a note\nsecond line ")!!
        assertEquals("text/plain", share.mimeType)
        assertFalse(share.hasFiles)
        assertEquals("a note\nsecond line", share.text)
    }

    @Test
    fun `a wildcard from the sender becomes a sane type`() {
        val share = ShareIntents.parse(ShareIntents.ACTION_SEND, "*/*", listOf("content://a"), null)!!
        assertEquals(ShareIntents.FALLBACK_MIME, share.mimeType)
        val text = ShareIntents.parse(ShareIntents.ACTION_SEND, "*/*", emptyList(), "hello")!!
        assertEquals(ShareIntents.TEXT_MIME, text.mimeType)
    }

    @Test
    fun `nothing to send is null`() {
        assertNull(ShareIntents.parse(ShareIntents.ACTION_SEND, "text/plain", emptyList(), null))
        assertNull(ShareIntents.parse(ShareIntents.ACTION_SEND, "text/plain", emptyList(), "   "))
        assertNull(ShareIntents.parse(ShareIntents.ACTION_SEND, "text/plain", listOf("  "), ""))
    }

    @Test
    fun `another action is not a share`() {
        assertNull(ShareIntents.parse("android.intent.action.VIEW", "text/plain", listOf("content://a"), null))
        assertNull(ShareIntents.parse(null, null, listOf("content://a"), null))
    }

    @Test
    fun `a pasted text gets a file name from its first line`() {
        assertEquals("Groceries.txt", ShareIntents.nameFor("Groceries\nmilk"))
        assertNull(ShareIntents.nameFor("   \n"))
        assertNull(ShareIntents.nameFor(null))
        val long = "x".repeat(200)
        assertTrue(ShareIntents.nameFor(long)!!.endsWith("….txt"))
    }
}
