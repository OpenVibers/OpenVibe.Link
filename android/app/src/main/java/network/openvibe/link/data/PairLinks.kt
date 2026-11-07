package network.openvibe.link.data

import java.net.URLDecoder
import java.net.URLEncoder

/**
 * The two links the app answers: `openvibe-link://pair?code=…` (the QR on openvibe.bot/link/phone) and
 * `openvibe-link://auth?code=…&state=…` (the sign-in's return). Parsed here, away from Android, so the shapes have
 * tests.
 */
data class PairLink(val code: String?, val network: String?, val pairing: String? = null)

object PairLinks {
    const val PAIR_SCHEME = "openvibe-link"
    const val PAIR_HOST = "pair"
    const val AUTH_HOST = "auth"
    const val DEFAULT_NETWORK = "https://openvibe.network"

    fun parse(uri: String?): PairLink? {
        if (uri.isNullOrBlank()) return null
        val trimmed = uri.trim()
        val prefix = "$PAIR_SCHEME://$PAIR_HOST"
        if (!trimmed.startsWith(prefix, ignoreCase = true)) return null
        val query = trimmed.substringAfter('?', missingDelimiterValue = "")
        val params = queryParams(query)
        return PairLink(
            code = params["code"]?.takeIf { it.isNotBlank() },
            network = params["network"]?.takeIf { it.startsWith("https://") } ?: DEFAULT_NETWORK,
            pairing = params["pairing"]?.takeIf { it.startsWith("pair_") },
        )
    }

    /** A scanned QR: our link, or the code itself, or a page whose `code` query carries it. */
    fun codeFromScan(contents: String?): PairLink? {
        val text = contents?.trim().orEmpty()
        if (text.isEmpty()) return null
        parse(text)?.let { if (it.code != null) return it }
        val params = queryParams(text.substringAfter('?', missingDelimiterValue = ""))
        params["code"]?.takeIf { it.isNotBlank() }?.let { code ->
            return PairLink(
                code,
                params["network"]?.takeIf { it.startsWith("https://") } ?: DEFAULT_NETWORK,
                params["pairing"]?.takeIf { it.startsWith("pair_") },
            )
        }
        val bare = text.replace("-", "").replace(" ", "")
        return if (bare.length == 8 && bare.all { it.isLetterOrDigit() }) PairLink(text, DEFAULT_NETWORK) else null
    }

    private fun queryParams(query: String): Map<String, String> {
        if (query.isBlank()) return emptyMap()
        return query.split('&').mapNotNull { part ->
            if (part.isBlank()) return@mapNotNull null
            val key = part.substringBefore('=')
            val value = part.substringAfter('=', missingDelimiterValue = "")
            if (key.isBlank()) return@mapNotNull null
            URLDecoder.decode(key, "UTF-8") to URLDecoder.decode(value, "UTF-8")
        }.toMap()
    }

    fun encode(value: String): String = URLEncoder.encode(value, "UTF-8")
}
