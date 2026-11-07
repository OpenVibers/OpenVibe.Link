package network.openvibe.link

import network.openvibe.link.share.ShareIntents
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The share sheet asks before it sends: nothing is uploaded on the intent alone, so the question says what will go and
 * Cancel sends nothing (ShareActivity's ConfirmShare). The reading rules themselves are in ShareIntentsTest.
 */
class ShareQuestionTest {
    private fun request(action: String, mime: String?, uris: List<String>, text: String? = null) =
        ShareIntents.parse(action, mime, uris, text)

    @Test
    fun `one file asks about one file`() {
        val r = request(ShareIntents.ACTION_SEND, "image/png", listOf("content://a/1"))!!
        assertEquals("Send this to your Download?", r.question)
    }

    @Test
    fun `several files say how many`() {
        val r = request(ShareIntents.ACTION_SEND_MULTIPLE, "image/*", listOf("content://a/1", "content://a/2", "content://a/3"))!!
        assertEquals("Send these 3 files to your Download?", r.question)
    }

    @Test
    fun `pasted text asks about text`() {
        val r = request(ShareIntents.ACTION_SEND, "text/plain", emptyList(), "hello")!!
        assertEquals("Send this text to your Download?", r.question)
    }

    @Test
    fun `an empty share has no question because there is nothing to ask`() {
        assertNull(request(ShareIntents.ACTION_SEND, "text/plain", emptyList(), "   "))
        assertNull(request("android.intent.action.VIEW", "text/plain", listOf("content://a/1")))
        assertNotNull(request(ShareIntents.ACTION_SEND, "*/*", listOf("content://a/1")))
        assertTrue(request(ShareIntents.ACTION_SEND, "image/png", listOf("content://a/1"))!!.hasFiles)
    }
}
