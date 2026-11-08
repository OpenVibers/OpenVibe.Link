package network.openvibe.link.data

import android.content.Context
import android.location.Location
import android.net.Uri
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import kotlin.math.max
import kotlin.math.round
import java.util.concurrent.TimeUnit

/** One item of the Action Center (`common.action-item@1`), which is what the Dock's activity tab draws. */
@Serializable
data class ActionItem(
    val service: String = "",
    val id: String = "",
    val kind: String = "",
    val title: String = "",
    val summary: String? = null,
    val bucket: String = "waiting",
    val state: String? = null,
    val agent: String? = null,
    @SerialName("created_at") val createdAt: String? = null,
    @SerialName("updated_at") val updatedAt: String? = null,
    @SerialName("next_run_at") val nextRunAt: String? = null,
    val error: String? = null,
    val href: String? = null,
    val actions: List<String> = emptyList(),
    /** Actor's own category, when it sends one (money, password, delete, new_recipient). */
    val category: String? = null,
) {
    val isApproval: Boolean get() = kind == "approval"
    val isPending: Boolean get() = bucket == "waiting" || state == "pending"
    val deviceKind: String? get() = when {
        summary?.startsWith("device.camera · ") == true -> "camera"
        summary?.startsWith("device.location") == true -> "location"
        else -> null
    }
    val deviceWhy: String? get() = if (deviceKind != null) summary?.substringAfter(" · ") else null
    val preciseRequested: Boolean get() = summary?.startsWith("device.location.precise · ") == true
    val isNotify: Boolean get() = service == "actor" && id.startsWith("ntf_") && summary?.startsWith("notify · ") == true
}

/** `network.me-actions-result@1`: the buckets, the counts, and whether a source did not answer. */
@Serializable
data class ActionsDoc(
    val buckets: Map<String, List<ActionItem>> = emptyMap(),
    val counts: Map<String, Int> = emptyMap(),
    val partial: Boolean = false,
)

class ApiException(message: String) : Exception(message)

/**
 * The person's own reads and answers on OpenVibe.Network, with the session token. Every route is under `/me`, and the
 * subject comes from the token, so this app can never read or answer another person's work.
 */
object Api {
    /** The server this build signs in and pairs with (Servers): a deep link never chooses it. */
    val NETWORK: String get() = Servers.network
    const val BOT = "https://openvibe.bot"
    const val DOWNLOAD = "https://openvibe.download"
    const val ACTOR = "https://openvibe.actor"

    const val APPROVE = "approve"
    const val DECLINE = "decline"

    private val json = Json { ignoreUnknownKeys = true }
    private val client = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .build()

    private val JSON_TYPE = "application/json; charset=utf-8".toMediaType()

    suspend fun actions(): ActionsDoc = withContext(Dispatchers.IO) {
        val token = Session.token() ?: throw ApiException("Sign in to OpenVibe to see your approvals.")
        val request = Request.Builder()
            .url("$NETWORK/api/v1/me/actions")
            .header("Authorization", "Bearer $token")
            .header("Accept", "application/json")
            .get()
            .build()
        client.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            if (response.code == 401) {
                Store.clearTokens()
                throw ApiException("Your session ended. Sign in again.")
            }
            if (!response.isSuccessful) throw ApiException("OpenVibe answered ${response.code}.")
            try {
                json.decodeFromString<ActionsDoc>(text)
            } catch (e: Exception) {
                throw ApiException("OpenVibe's answer could not be read.")
            }
        }
    }

    /** A short heartbeat only while Link's paired connection is live; Actor treats it as offline after 60 seconds. */
    suspend fun devicePresence(deviceId: String) = withContext(Dispatchers.IO) {
        if (!deviceId.matches(Regex("dev_[0-9A-HJKMNP-TV-Z]{26}"))) return@withContext
        val token = Session.token() ?: return@withContext
        val body = JSONObject().put("device_id", deviceId).toString()
        val request = Request.Builder().url("$ACTOR/api/v1/me/devices/presence")
            .header("Authorization", "Bearer $token")
            .post(body.toRequestBody(JSON_TYPE)).build()
        client.newCall(request).execute().use { /* a missed heartbeat makes the phone offline after a minute */ }
    }

    /** Answer one Actor approval as the person themself (the same decision /me/approvals makes). */
    suspend fun decide(item: ActionItem, approve: Boolean): ActionItem = withContext(Dispatchers.IO) {
        if (item.service != "actor" || !item.isApproval) throw ApiException("This item is not decided here.")
        val token = Session.token() ?: throw ApiException("Sign in to OpenVibe to answer approvals.")
        val action = if (approve) APPROVE else DECLINE
        val url = "$NETWORK/api/v1/me/actions/${item.service}/${item.id}/$action"
        val request = Request.Builder()
            .url(url)
            .header("Authorization", "Bearer $token")
            .header("Accept", "application/json")
            .post("{}".toRequestBody(JSON_TYPE))
            .build()
        client.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            if (response.code == 401) {
                Store.clearTokens()
                throw ApiException("Your session ended. Sign in again.")
            }
            if (!response.isSuccessful) throw ApiException(problem(text) ?: "OpenVibe answered ${response.code}.")
            val body = try {
                json.parseToJsonElement(text)
            } catch (e: Exception) {
                throw ApiException("OpenVibe's answer could not be read.")
            }
            val itemJson = (body as? kotlinx.serialization.json.JsonObject)?.get("item") ?: body
            try {
                json.decodeFromJsonElement(ActionItem.serializer(), itemJson)
            } catch (e: Exception) {
                item.copy(state = action)
            }
        }
    }

    /** Device answers bypass Network's approve/decline shortcut: Actor checks the one photo or location itself. */
    suspend fun answerDevice(item: ActionItem, answer: JSONObject) = withContext(Dispatchers.IO) {
        if (item.service != "actor" || item.deviceKind == null) throw ApiException("This is not a device request.")
        val token = Session.token() ?: throw ApiException("Sign in to answer your agent.")
        val request = Request.Builder().url("$ACTOR/api/v1/me/approvals/${item.id}/answer")
            .header("Authorization", "Bearer $token").header("Accept", "application/json")
            .post(answer.toString().toRequestBody(JSON_TYPE)).build()
        client.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            if (!response.isSuccessful) throw ApiException(problem(text) ?: "Actor answered ${response.code}.")
        }
    }

    /** The system camera's one output; Actor validates, strips metadata and binds it to this approval. */
    suspend fun uploadPhoto(item: ActionItem, context: Context, uri: Uri): String = withContext(Dispatchers.IO) {
        if (item.deviceKind != "camera") throw ApiException("This is not a photo request.")
        val token = Session.token() ?: throw ApiException("Sign in to send your photo.")
        val bytes = context.contentResolver.openInputStream(uri)?.use { stream ->
            val output = ByteArrayOutputStream()
            val chunk = ByteArray(64 * 1024)
            while (true) {
                val count = stream.read(chunk)
                if (count < 0) break
                if (output.size() + count > 10 * 1024 * 1024) throw ApiException("The photo is over 10 MB.")
                output.write(chunk, 0, count)
            }
            output.toByteArray()
        } ?: throw ApiException("The camera did not return a photo.")
        if (bytes.isEmpty()) throw ApiException("The camera did not return a photo.")
        val request = Request.Builder().url("$ACTOR/api/v1/me/approvals/${item.id}/photo")
            .header("Authorization", "Bearer $token").header("Accept", "application/json")
            .post(bytes.toRequestBody("image/jpeg".toMediaType())).build()
        client.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            if (!response.isSuccessful) throw ApiException(problem(text) ?: "Actor answered ${response.code}.")
            JSONObject(text).optString("media_id").takeIf { it.startsWith("med_") }
                ?: throw ApiException("Actor did not return a photo id.")
        }
    }

    fun photoAnswer(mediaId: String): JSONObject = JSONObject().put("decision", "answer")
        .put("photo", JSONObject().put("media_id", mediaId))

    fun locationAnswer(location: Location, precise: Boolean): JSONObject {
        val latitude = if (precise) location.latitude else round(location.latitude * 100.0) / 100.0
        val longitude = if (precise) location.longitude else round(location.longitude * 100.0) / 100.0
        return JSONObject().put("decision", "answer").put("location", JSONObject()
            .put("lat", latitude).put("lng", longitude)
            .put("accuracy_m", if (precise) location.accuracy.toDouble() else max(1000.0, location.accuracy.toDouble()))
            .put("precise", precise))
    }

    private fun problem(text: String): String? = try {
        (json.parseToJsonElement(text) as? kotlinx.serialization.json.JsonObject)
            ?.get("detail")?.toString()?.trim('"')?.takeIf { it.isNotEmpty() }
    } catch (e: Exception) {
        null
    }
}
