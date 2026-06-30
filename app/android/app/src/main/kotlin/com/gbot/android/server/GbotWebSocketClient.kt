package com.gbot.android.server

import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Environment
import android.provider.MediaStore
import android.util.Log
import com.google.gson.Gson
import com.google.gson.JsonObject
import com.google.gson.JsonSyntaxException
import com.gbot.android.model.CommandRequest
import com.gbot.android.model.CommandResponse
import com.gbot.android.service.MobileAccessibilityService
import org.java_websocket.client.WebSocketClient
import org.java_websocket.handshake.ServerHandshake
import java.io.File
import java.io.OutputStream
import java.net.URI
import java.nio.ByteBuffer
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors

/**
 * GbotWebSocketClient is the device-side WebSocket CLIENT: it dials the gbot
 * daemon (ws://host:8765/ws) and stays connected for the lifetime of the
 * foreground service. The reconnect loop lives in ConnectionForegroundService;
 * this class only owns one connection's command/response protocol.
 *
 * The on-wire protocol is identical to the old server: gbot sends a
 * CommandRequest, this client dispatches it (file ops inline, UI ops via the
 * accessibility service) and replies with a CommandResponse. send_file streams
 * binary frames bracketed by receive_file_begin/end; the MediaStore write +
 * APK install logic is preserved verbatim from the server implementation.
 */
class GbotWebSocketClient(
    private val serverUri: URI,
    private val onLog: (String) -> Unit,
    private val onConnectionChange: (Int) -> Unit
) : WebSocketClient(serverUri) {

    companion object {
        private const val TAG = "GbotWSClient"

        // The MIME type drives the install intent (APK) and the MediaStore row; getting
        // it wrong makes the package installer reject the file silently.
        fun mimeTypeFor(name: String): String {
            val lower = name.substringAfterLast('.', "").lowercase()
            return when (lower) {
                "apk" -> "application/vnd.android.package-archive"
                "txt" -> "text/plain"
                "jpg", "jpeg" -> "image/jpeg"
                "png" -> "image/png"
                else -> "application/octet-stream"
            }
        }
    }

    private val gson = Gson()
    private val executor = Executors.newFixedThreadPool(4)

    // MediaStore writes need an application context; the foreground service
    // sets this before connect. Throw clearly if unset — the service always
    // sets it, so a null here is a programmer error, not a runtime condition.
    private var appContext: Context? = null

    fun setContext(ctx: Context) {
        appContext = ctx.applicationContext
    }

    // Single in-flight file transfer. In the client direction there is only one
    // connection (this), so the transfer is keyed on `this` rather than a conn.
    private data class FileTransferSession(
        var output: OutputStream? = null,
        var contentUri: Uri? = null,
        var path: String = "",
        var expectedSize: Long = 0L,
        var bytesWritten: Long = 0L
    )
    private var transfer: FileTransferSession? = null
    private val transfersLock = Any()

    internal var openDownloadStream: (name: String, size: Long) -> DownloadSink? =
        { name, size -> openMediaStoreDownload(name, size) }
    internal val downloads = mutableMapOf<String, OutputStream>()
    internal data class DownloadSink(val output: OutputStream, val uri: Uri)

    init {
        isReuseAddr = true
    }

    private val closeLatch = CountDownLatch(1)

    /**
     * awaitClose blocks the caller until onClose fires. The reconnect loop in
     * ConnectionForegroundService calls this to wait for a peer-initiated close
     * WITHOUT sending a close frame (closeBlocking() would initiate one).
     */
    fun awaitClose() {
        closeLatch.await()
    }

    override fun onOpen(handshakedata: ServerHandshake?) {
        Log.i(TAG, "Connected to gbot at $serverUri")
        onLog("Connected to gbot at $serverUri")
        onConnectionChange(1)
    }

    override fun onClose(code: Int, reason: String?, remote: Boolean) {
        Log.i(TAG, "Disconnected from gbot: $reason")
        onLog("Disconnected from gbot: $reason")
        // A dropped connection mid-transfer must free its output stream.
        val session = synchronized(transfersLock) { transfer }
        session?.output?.close()
        synchronized(transfersLock) { transfer = null }
        onConnectionChange(0)
        closeLatch.countDown()
    }

    override fun onMessage(message: String) {
        executor.submit {
            try {
                val request = gson.fromJson(message, CommandRequest::class.java)
                if (request.command == null) {
                    sendError(null, "Missing 'command' field")
                    return@submit
                }

                // File transfer commands bypass the accessibility service — they
                // are file-system ops, not UI automation.
                when (request.command) {
                    "receive_file_begin" -> { handleFileBegin(request); return@submit }
                    "receive_file_end" -> { handleFileEnd(request); return@submit }
                }

                onLog(">> ${request.command}" +
                    if (request.params != null) " ${request.params}" else "")

                val service = MobileAccessibilityService.instance
                if (service == null) {
                    sendError(request.id, "Accessibility service is not running. Enable it in Settings.")
                    return@submit
                }

                val response = service.handleCommand(request)
                val finalResponse = response.copy(id = request.id)
                val json = gson.toJson(finalResponse)
                send(json)

                onLog("<< ${request.command}: ${if (finalResponse.success) "OK" else "ERR"}")

            } catch (e: JsonSyntaxException) {
                sendError(null, "Invalid JSON: ${e.message}")
            } catch (e: Exception) {
                Log.e(TAG, "Error processing message", e)
                sendError(null, "Internal error: ${e.message}")
            }
        }
    }

    override fun onMessage(bytes: ByteBuffer) {
        // Copy the ByteBuffer BEFORE entering the synchronized block: org.java_websocket
        // reuses/pools buffers, so we must materialize the bytes before the frame
        // returns. Doing the copy outside the lock keeps the critical section short.
        val copy = ByteArray(bytes.remaining())
        bytes.get(copy)

        // The write + counter mutation MUST be inside synchronized(transfersLock):
        // the library can dispatch binary frames across multiple executor threads,
        // so two frames can run onMessage concurrently. Mutating session.output and
        // session.bytesWritten outside the lock would be a data race (torn writes,
        // lost counter increments).
        synchronized(transfersLock) {
            val session = transfer
            if (session == null) {
                onLog("!! binary frame with no active transfer (${copy.size} bytes)")
                return
            }
            try {
                session.output?.write(copy)
                session.bytesWritten += copy.size
            } catch (e: Exception) {
                Log.e(TAG, "binary write failed", e)
            }
        }
    }

    override fun onError(ex: Exception) {
        Log.e(TAG, "WebSocket error", ex)
        onLog("Error: ${ex.message}")
    }

    private fun handleFileBegin(request: CommandRequest) {
        val relPath = request.params?.get("path")?.asString
            ?: return sendError(request.id, "receive_file_begin requires 'path'")
        val size = request.params.get("size")?.asLong ?: 0L
        // Sanitize: basename only, reject traversal. The model never sends a
        // device path (Go sends filepath.Base), so any '/' or '..' is malformed.
        val name = File(relPath).name
        if (name.isEmpty() || name.contains("..")) {
            return sendError(request.id, "invalid path: $relPath")
        }
        try {
            val sink = openDownloadStream(name, size)
                ?: return sendError(request.id, "open output: MediaStore insert failed")
            synchronized(transfersLock) {
                transfer = FileTransferSession(
                    output = sink.output,
                    contentUri = sink.uri,
                    path = name,
                    expectedSize = size,
                    bytesWritten = 0L
                )
            }
        } catch (e: Exception) {
            return sendError(request.id, "open output: ${e.message}")
        }
        onLog(">> receive_file_begin: $name ($size bytes)")
        send(gson.toJson(CommandResponse.success(request.id, JsonObject().apply {
            addProperty("path", name)
        })))
    }

    private fun handleFileEnd(request: CommandRequest) {
        val session = synchronized(transfersLock) { val s = transfer; transfer = null; s }
        val out = session?.output
        if (out == null) {
            return sendError(request.id, "receive_file_end without an active transfer")
        }
        out.close()
        if (session.expectedSize > 0 && session.bytesWritten != session.expectedSize) {
            return sendError(request.id,
                "size mismatch: wrote ${session.bytesWritten}, expected ${session.expectedSize}")
        }
        val isApk = session.path.endsWith(".apk", ignoreCase = true)
        if (isApk) {
            val uri = session.contentUri
                ?: return sendError(request.id, "install failed: missing content uri")
            installFromUri(uri)
        }
        onLog("<< receive_file_end: ${session.path} (${session.bytesWritten} bytes)${if (isApk) " + install" else ""}")
        send(gson.toJson(CommandResponse.success(request.id, JsonObject().apply {
            addProperty("bytes", session.bytesWritten)
            addProperty("installed", isApk)
        })))
    }

    // MediaStore.Downloads publishes the file under the public Downloads/gbot
    // directory so it shows in the user's file manager. RELATIVE_PATH places the
    // row; openOutputStream returns the streaming sink that binary frames write to.
    private fun openMediaStoreDownload(name: String, size: Long): DownloadSink? {
        val ctx = appContext
            ?: throw IllegalStateException("appContext not set; call setContext() before connect")
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, name)
            put(MediaStore.MediaColumns.MIME_TYPE, mimeTypeFor(name))
            put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/gbot")
            if (size > 0) put(MediaStore.MediaColumns.SIZE, size)
        }
        val uri = ctx.contentResolver.insert(
            MediaStore.Downloads.EXTERNAL_CONTENT_URI, values
        ) ?: return null
        val output = ctx.contentResolver.openOutputStream(uri, "w") ?: return null
        return DownloadSink(output, uri)
    }

    private fun installFromUri(uri: Uri) {
        val ctx = appContext ?: return
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        ctx.startActivity(intent)
    }

    private fun sendError(id: String?, message: String) {
        val response = CommandResponse.error(id, message)
        send(gson.toJson(response))
        onLog("<< ERROR: $message")
    }

    fun shutdown() {
        try {
            executor.shutdownNow()
            close()
        } catch (e: Exception) {
            Log.e(TAG, "Error shutting down", e)
        }
    }
}
