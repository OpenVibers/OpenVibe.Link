package network.openvibe.link.share

/**
 * What a share sheet target received, without an Android type in it so the reading rules can be tested on the JVM.
 *
 * `uris` are content URIs as strings (the app turns them back into Uri when it opens them). `text` is the pasted
 * text of a text share; a share with both files and text sends the files and keeps the text as the name.
 */
data class ShareRequest(
    val mimeType: String,
    val uris: List<String>,
    val text: String?,
) {
    val isMultiple: Boolean get() = uris.size > 1
    val hasFiles: Boolean get() = uris.isNotEmpty()
}

object ShareIntents {
    const val ACTION_SEND = "android.intent.action.SEND"
    const val ACTION_SEND_MULTIPLE = "android.intent.action.SEND_MULTIPLE"
    const val FALLBACK_MIME = "application/octet-stream"
    const val TEXT_MIME = "text/plain"

    /**
     * Read one share. Null means there is nothing to send (or it is not a share at all), so the app can say so
     * instead of opening an empty upload.
     */
    fun parse(action: String?, mimeType: String?, uris: List<String>, text: String?): ShareRequest? {
        val multiple = when (action) {
            ACTION_SEND -> false
            ACTION_SEND_MULTIPLE -> true
            else -> return null
        }
        val files = uris.map { it.trim() }.filter { it.isNotEmpty() }.distinct()
        val pasted = text?.trim()?.takeIf { it.isNotEmpty() }
        if (files.isEmpty() && pasted == null) return null

        val mime = mimeType?.trim()?.lowercase()?.takeIf { it.isNotEmpty() && it != "*/*" }
            ?: if (files.isEmpty()) TEXT_MIME else FALLBACK_MIME
        val chosen = if (multiple) files else files.take(1)
        return ShareRequest(mimeType = mime, uris = chosen, text = pasted)
    }

    /** A pasted text has no file name: the first line, clipped, is the file's name. */
    fun nameFor(text: String?): String? {
        val first = text?.lineSequence()?.firstOrNull()?.trim()?.takeIf { it.isNotEmpty() } ?: return null
        return (first.take(60) + if (first.length > 60) "…" else "") + ".txt"
    }
}
