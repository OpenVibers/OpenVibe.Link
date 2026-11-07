package network.openvibe.link.share

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import network.openvibe.link.data.Api
import network.openvibe.link.data.ApiException
import network.openvibe.link.data.Session
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.InputStream
import java.util.concurrent.TimeUnit

/**
 * "Send to OpenVibe": the drive's own upload, the same three steps the web page makes to OpenVibe.MediaHub
 * (POST /api/v1/files → PUT /api/v1/uploads/:id/parts/:n → POST …/complete), with the person's Network token as the
 * Bearer. The bytes go straight from the phone: nothing is re-encoded, and the file is private until the person
 * shares it on openvibe.download.
 */
object ShareUploader {
    private val client = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .writeTimeout(120, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .build()

    private val JSON = "application/json; charset=utf-8".toMediaType()
    private val OCTETS = "application/octet-stream".toMediaType()

    class Sent(val fileId: String?, val name: String)

    /** Size and display name of a shared content Uri, as the system reports them. */
    fun describe(context: Context, uri: Uri): Pair<Long, String> {
        var name = uri.lastPathSegment?.substringAfterLast('/')?.takeIf { it.isNotBlank() } ?: "file"
        var size = -1L
        try {
            context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
                if (c.moveToFirst()) {
                    val nameIndex = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                    val sizeIndex = c.getColumnIndex(OpenableColumns.SIZE)
                    if (nameIndex >= 0) name = c.getString(nameIndex)?.takeIf { it.isNotBlank() } ?: name
                    if (sizeIndex >= 0 && !c.isNull(sizeIndex)) size = c.getLong(sizeIndex)
                }
            }
        } catch (e: Exception) {
            // A provider that refuses metadata: the stream's own size is asked for below.
        }
        if (size <= 0) {
            size = try {
                context.contentResolver.openFileDescriptor(uri, "r")?.use { it.statSize } ?: -1L
            } catch (e: Exception) {
                -1L
            }
        }
        if (size <= 0) throw ApiException("This file could not be read.")
        return size to name
    }

    suspend fun send(
        context: Context,
        uri: Uri,
        mimeType: String,
        onProgress: (sent: Long, total: Long) -> Unit,
    ): Sent = withContext(Dispatchers.IO) {
        val token = Session.token() ?: throw ApiException("Sign in to OpenVibe to send files.")
        val (size, name) = describe(context, uri)
        val upload = start(token, name, size, mimeType)

        val input = context.contentResolver.openInputStream(uri)
            ?: throw ApiException("This file could not be opened.")
        input.use { stream ->
            var sent = 0L
            for (part in 1..upload.parts) {
                val length = if (part < upload.parts) upload.partSize else (size - upload.partSize * (upload.parts - 1))
                val bytes = readExactly(stream, length.toInt())
                if (bytes.size != length.toInt()) throw ApiException("The file changed while it was being sent.")
                putPart(token, upload.id, part, bytes)
                sent += length
                onProgress(sent, size)
            }
        }
        val fileId = complete(token, upload.id)
        Sent(fileId, name)
    }

    private class Started(val id: String, val partSize: Long, val parts: Long)

    private fun start(token: String, name: String, size: Long, mimeType: String): Started {
        val body = JSONObject()
            .put("name", name)
            .put("size", size)
            .put("content_type", mimeType.ifBlank { "application/octet-stream" })
            .toString()
            .toRequestBody(JSON)
        val request = Request.Builder()
            .url("${Api.DOWNLOAD}/api/v1/files")
            .header("Authorization", "Bearer $token")
            .header("Accept", "application/json")
            .post(body)
            .build()
        client.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            if (response.code == 401) throw ApiException("Your session ended. Sign in again.")
            if (!response.isSuccessful) throw ApiException(problem(text) ?: "The drive refused the upload (${response.code}).")
            val upload = JSONObject(text).optJSONObject("upload")
                ?: throw ApiException("The drive answered no upload.")
            val id = upload.optString("id")
            if (id.isEmpty()) throw ApiException("The drive answered no upload.")
            return Started(id, upload.optLong("part_size", 8 * 1024 * 1024L), upload.optLong("parts_expected", 1L))
        }
    }

    private fun putPart(token: String, uploadId: String, part: Long, bytes: ByteArray) {
        val request = Request.Builder()
            .url("${Api.DOWNLOAD}/api/v1/uploads/$uploadId/parts/$part")
            .header("Authorization", "Bearer $token")
            .put(bytes.toRequestBody(OCTETS))
            .build()
        client.newCall(request).execute().use { response ->
            if (!response.isSuccessful) {
                val text = response.body?.string().orEmpty()
                throw ApiException(problem(text) ?: "A part of the file was refused (${response.code}).")
            }
        }
    }

    private fun complete(token: String, uploadId: String): String? {
        val request = Request.Builder()
            .url("${Api.DOWNLOAD}/api/v1/uploads/$uploadId/complete")
            .header("Authorization", "Bearer $token")
            .header("Accept", "application/json")
            .post("{}".toRequestBody(JSON))
            .build()
        client.newCall(request).execute().use { response ->
            val text = response.body?.string().orEmpty()
            if (!response.isSuccessful) throw ApiException(problem(text) ?: "The drive could not finish the upload (${response.code}).")
            return try {
                JSONObject(text).optJSONObject("file")?.optString("id")?.takeIf { it.isNotEmpty() }
            } catch (e: Exception) {
                null
            }
        }
    }

    private fun readExactly(stream: InputStream, length: Int): ByteArray {
        val out = ByteArray(length)
        var read = 0
        while (read < length) {
            val n = stream.read(out, read, length - read)
            if (n < 0) break
            read += n
        }
        return if (read == length) out else out.copyOf(read)
    }

    private fun problem(text: String): String? = try {
        JSONObject(text).optString("detail").takeIf { it.isNotEmpty() }
    } catch (e: Exception) {
        null
    }

    /** A pasted text becomes a small file in the app's cache, named after its first line. */
    fun textFile(context: Context, text: String): Pair<Uri, String> {
        val name = ShareIntents.nameFor(text) ?: "note.txt"
        val file = java.io.File(context.cacheDir, "share-${System.currentTimeMillis()}-$name")
        file.writeText(text)
        return Uri.fromFile(file) to name
    }
}
