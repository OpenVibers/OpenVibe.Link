package network.openvibe.link.data

import java.net.URLDecoder
import java.net.URLEncoder

/**
 * The two links the app answers: `openvibe-link://pair?code=…` (the QR on openvibe.bot/link/phone) and the sign-in's
 * return, an https App Link for `https://openvibe.network/link/auth` (MainActivity). Parsed here, away from Android,
 * so the shapes have tests.
 *
 * The pair link names no server, and if it names one it must be this build's (Servers.network): a link that points
 * elsewhere is refused, never followed, because the phone would otherwise send its pairing to whoever wrote the link.
 */
data class PairLink(
    val code: String?,
    val network: String?,
    val pairing: String? = null,
    /** The server a refused link named (its host when it parses, the raw value otherwise); null when the link is ours. */
    val refusedServer: String? = null,
)

object PairLinks {
    const val PAIR_SCHEME = "openvibe-link"
    const val PAIR_HOST = "pair"
    const val AUTH_PATH = "/link/auth"

    fun parse(uri: String?, accepted: String = Servers.network): PairLink? {
        if (uri.isNullOrBlank()) return null
        val trimmed = uri.trim()
        val prefix = "$PAIR_SCHEME://$PAIR_HOST"
        if (!trimmed.startsWith(prefix, ignoreCase = true)) return null
        val params = queryParams(trimmed.substringAfter('?', missingDelimiterValue = ""))
        return link(params, accepted)
    }

    /** A scanned QR: our link, the code itself, or a page whose `code` query carries it. */
    fun codeFromScan(contents: String?, accepted: String = Servers.network): PairLink? {
        val text = contents?.trim().orEmpty()
        if (text.isEmpty()) return null
        parse(text, accepted)?.let { if (it.code != null || it.refusedServer != null) return it }
        val params = queryParams(text.substringAfter('?', missingDelimiterValue = ""))
        if (params["code"]?.isNotBlank() == true) return link(params, accepted)
        val bare = text.replace("-", "").replace(" ", "")
        return if (bare.length == 8 && bare.all { it.isLetterOrDigit() }) PairLink(text, accepted) else null
    }

    /** The link's code, pairing id and server — refused whole when the server is someone else's. */
    private fun link(params: Map<String, String>, accepted: String): PairLink {
        val named = params["network"]?.takeIf { it.isNotBlank() }
        if (named != null && !Servers.same(named, accepted)) {
            return PairLink(code = null, network = accepted, refusedServer = Servers.normalize(named)?.let { hostOf(it) } ?: named)
        }
        return PairLink(
            code = params["code"]?.takeIf { it.isNotBlank() },
            network = accepted,
            pairing = params["pairing"]?.takeIf { it.startsWith("pair_") },
        )
    }

    private fun hostOf(url: String): String {
        val rest = url.substringAfter("://", missingDelimiterValue = "")
        return rest.substringBefore('/').substringBefore(':').ifBlank { url }
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
