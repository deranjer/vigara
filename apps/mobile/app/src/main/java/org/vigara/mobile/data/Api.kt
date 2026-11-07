package org.vigara.mobile.data

import android.content.Context
import android.content.SharedPreferences
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import okhttp3.Cookie
import okhttp3.CookieJar
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.io.IOException
import java.util.concurrent.TimeUnit

/** An error response from the server, carrying the message the web UI would show. */
class ApiException(val status: Int, message: String) : IOException(message)

val AppJson = Json {
    ignoreUnknownKeys = true
    coerceInputValues = true
    explicitNulls = false
    encodeDefaults = true
    isLenient = true
}

/** Keeps the server's session cookie across restarts. Only cookies for the signed-in server are stored. */
class PersistentCookieJar(private val prefs: SharedPreferences) : CookieJar {
    private val cookies = mutableListOf<Cookie>()

    init {
        val base = prefs.getString(KEY_SERVER, null)?.toHttpUrlOrNull()
        val saved = prefs.getStringSet(KEY_COOKIES, emptySet()).orEmpty()
        if (base != null) saved.mapNotNullTo(cookies) { Cookie.parse(base, it) }
    }

    @Synchronized
    override fun saveFromResponse(url: HttpUrl, cookies: List<Cookie>) {
        for (cookie in cookies) {
            this.cookies.removeAll { it.name == cookie.name && it.domain == cookie.domain && it.path == cookie.path }
            if (cookie.expiresAt > System.currentTimeMillis()) this.cookies.add(cookie)
        }
        persist()
    }

    @Synchronized
    override fun loadForRequest(url: HttpUrl): List<Cookie> {
        val now = System.currentTimeMillis()
        if (cookies.removeAll { it.expiresAt <= now }) persist()
        return cookies.filter { it.matches(url) }
    }

    @Synchronized
    fun clear() {
        cookies.clear()
        persist()
    }

    private fun persist() {
        prefs.edit().putStringSet(KEY_COOKIES, cookies.map { it.toString() }.toSet()).apply()
    }

    companion object {
        const val KEY_SERVER = "server"
        const val KEY_COOKIES = "cookies"
    }
}

/**
 * The only network entry point. Authentication is the server's HttpOnly session cookie, exactly as
 * the web UI uses, so no server changes are needed. Every call runs on the IO dispatcher.
 */
class VigaraApi(context: Context) {
    private val prefs = context.getSharedPreferences("vigara_session", Context.MODE_PRIVATE)
    private val jar = PersistentCookieJar(prefs)

    /** Incremented whenever the server answers 401 so the UI can return to the sign-in screen. */
    val unauthorized = MutableStateFlow(0)

    private val http = OkHttpClient.Builder()
        .cookieJar(jar)
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .writeTimeout(30, TimeUnit.SECONDS)
        .pingInterval(25, TimeUnit.SECONDS)
        .followRedirects(false)
        .build()

    var serverUrl: String
        get() = prefs.getString(PersistentCookieJar.KEY_SERVER, "").orEmpty()
        private set(value) { prefs.edit().putString(PersistentCookieJar.KEY_SERVER, value).apply() }

    var lastUsername: String
        get() = prefs.getString("username", "").orEmpty()
        set(value) { prefs.edit().putString("username", value).apply() }

    val hasServer: Boolean get() = serverUrl.isNotEmpty()

    /** Normalises what the user typed: adds https:// when no scheme is present and drops path/trailing slash. */
    fun setServer(input: String): String? {
        var text = input.trim()
        if (text.isEmpty()) return null
        if (!text.contains("://")) text = "https://$text"
        val url = text.toHttpUrlOrNull() ?: return null
        val normalised = url.newBuilder().encodedPath("/").query(null).fragment(null).build().toString().trimEnd('/')
        if (normalised != serverUrl) jar.clear()
        serverUrl = normalised
        return normalised
    }

    fun forgetSession() = jar.clear()

    private fun url(path: String): HttpUrl =
        (serverUrl + path).toHttpUrlOrNull() ?: throw ApiException(0, "The server address is not valid.")

    private fun failure(response: Response, text: String): ApiException {
        if (response.code == 401 && !response.request.url.encodedPath.startsWith("/api/auth/login")) unauthorized.value++
        val detail = runCatching { AppJson.parseToJsonElement(text).jsonObject }.getOrNull()
        val message = (detail?.get("detail") as? JsonPrimitive)?.contentOrNull
            ?: (detail?.get("message") as? JsonPrimitive)?.contentOrNull
            ?: text.trim().take(400).ifEmpty { "The request failed (HTTP ${response.code})." }
        return ApiException(response.code, message)
    }

    /** Sends a request and returns the response body text. */
    suspend fun send(method: String, path: String, body: JsonElement? = null): String = withContext(Dispatchers.IO) {
        val builder = Request.Builder().url(url(path))
        val requestBody = when {
            body != null -> body.toString().toRequestBody(JSON_TYPE)
            method == "GET" -> null
            else -> "{}".toRequestBody(JSON_TYPE)
        }
        builder.method(method, requestBody)
        builder.header("Accept", "application/json")
        try {
            http.newCall(builder.build()).execute().use { response ->
                val text = response.body?.string().orEmpty()
                if (!response.isSuccessful) throw failure(response, text)
                text
            }
        } catch (error: ApiException) {
            throw error
        } catch (error: IOException) {
            throw ApiException(0, "Could not reach the server: ${error.message ?: error.javaClass.simpleName}")
        }
    }

    suspend inline fun <reified T> get(path: String): T = AppJson.decodeFromString(send("GET", path))

    suspend inline fun <reified T> post(path: String, body: JsonElement? = null): T {
        val text = send("POST", path, body)
        return AppJson.decodeFromString(text.ifBlank { "{}" })
    }

    suspend inline fun <reified T> put(path: String, body: JsonElement): T {
        val text = send("PUT", path, body)
        return AppJson.decodeFromString(text.ifBlank { "{}" })
    }

    suspend fun postUnit(path: String, body: JsonElement? = null) { send("POST", path, body) }
    suspend fun putUnit(path: String, body: JsonElement) { send("PUT", path, body) }
    suspend fun patchUnit(path: String, body: JsonElement) { send("PATCH", path, body) }
    suspend fun delete(path: String, body: JsonElement? = null) { send("DELETE", path, body) }

    /** Reads a newline-delimited JSON response (used for image pulls and container recreates). */
    suspend fun postLines(path: String, body: JsonElement, onLine: (JsonObject) -> Unit) = withContext(Dispatchers.IO) {
        val client = http.newBuilder().readTimeout(10, TimeUnit.MINUTES).build()
        val request = Request.Builder().url(url(path)).post(body.toString().toRequestBody(JSON_TYPE)).build()
        try {
            client.newCall(request).execute().use { response ->
                if (!response.isSuccessful) throw failure(response, response.body?.string().orEmpty())
                val source = response.body?.source() ?: return@use
                while (true) {
                    val line = source.readUtf8Line() ?: break
                    if (line.isBlank()) continue
                    val obj = runCatching { AppJson.parseToJsonElement(line).jsonObject }.getOrNull() ?: continue
                    onLine(obj)
                }
            }
        } catch (error: ApiException) {
            throw error
        } catch (error: IOException) {
            throw ApiException(0, "Lost connection: ${error.message ?: error.javaClass.simpleName}")
        }
    }

    /**
     * Opens one of the server's WebSocket streams (container logs and live stats). Each text frame
     * holds whole newline-delimited JSON records; the flow emits them parsed and completes when the
     * server closes the socket or the collector goes away.
     */
    fun stream(path: String): Flow<StreamEvent> = callbackFlow {
        // OkHttp upgrades http(s) URLs to ws(s) itself and sends the session cookie from the jar.
        val request = Request.Builder().url(url(path)).build()
        val socket: WebSocket = http.newBuilder().readTimeout(0, TimeUnit.MILLISECONDS).build()
            .newWebSocket(request, object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) { trySend(StreamEvent.Open) }
                override fun onMessage(webSocket: WebSocket, text: String) {
                    val records = text.lineSequence().filter { it.isNotBlank() }.mapNotNull {
                        runCatching { AppJson.parseToJsonElement(it).jsonObject }.getOrNull()
                    }.toList()
                    if (records.isNotEmpty()) trySend(StreamEvent.Records(records))
                }
                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) { trySend(StreamEvent.Closed(reason)); close() }
                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    if (response?.code == 401) unauthorized.value++
                    trySend(StreamEvent.Failed(response?.let { "HTTP ${it.code}" } ?: (t.message ?: "Connection lost")))
                    close()
                }
            })
        awaitClose { socket.cancel() }
    }

    /** The session cookie as a Cookie header, for WebViews that must share the sign-in. */
    fun cookiesFor(path: String): List<Cookie> = jar.loadForRequest(url(path))

    /** Sends a request and returns the response with headers; the caller streams the body to a file. */
    suspend fun download(method: String, path: String, body: JsonElement? = null, sink: (java.io.InputStream, Response) -> Unit) = withContext(Dispatchers.IO) {
        val client = http.newBuilder().readTimeout(10, TimeUnit.MINUTES).build()
        val builder = Request.Builder().url(url(path))
        builder.method(method, body?.toString()?.toRequestBody(JSON_TYPE))
        try {
            client.newCall(builder.build()).execute().use { response ->
                if (!response.isSuccessful) throw failure(response, response.body?.string().orEmpty())
                sink(response.body!!.byteStream(), response)
            }
        } catch (error: ApiException) {
            throw error
        } catch (error: IOException) {
            throw ApiException(0, "Download failed: ${error.message ?: error.javaClass.simpleName}")
        }
    }

    /** A bidirectional terminal socket: binary frames are terminal output, text frames are control (end) messages. */
    fun openTerminal(path: String, listener: TerminalListener): TerminalSocket {
        val request = Request.Builder().url(url(path)).build()
        val socket = http.newBuilder().readTimeout(0, TimeUnit.MILLISECONDS).build().newWebSocket(request, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) = listener.onOpen()
            override fun onMessage(webSocket: WebSocket, bytes: okio.ByteString) = listener.onOutput(bytes.toByteArray())
            override fun onMessage(webSocket: WebSocket, text: String) = listener.onText(text)
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = listener.onClosed(null)
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                if (response?.code == 401) unauthorized.value++
                listener.onClosed(response?.let { "The server refused the terminal (HTTP ${it.code})." } ?: (t.message ?: "Connection lost"))
            }
        })
        return TerminalSocket(socket)
    }

    private companion object {
        val JSON_TYPE = "application/json".toMediaType()
    }
}

sealed interface StreamEvent {
    data object Open : StreamEvent
    data class Records(val records: List<JsonObject>) : StreamEvent
    data class Closed(val reason: String) : StreamEvent
    data class Failed(val message: String) : StreamEvent
}

fun JsonObject.str(key: String): String? = (this[key] as? JsonPrimitive)?.contentOrNull

interface TerminalListener {
    fun onOpen()
    fun onOutput(bytes: ByteArray)
    fun onText(text: String)
    fun onClosed(error: String?)
}

class TerminalSocket(private val socket: WebSocket) {
    fun send(bytes: ByteArray) { socket.send(okio.ByteString.of(*bytes)) }
    fun resize(cols: Int, rows: Int) { socket.send("""{"type":"resize","cols":$cols,"rows":$rows}""") }
    fun close() { socket.close(1000, null) }
}
