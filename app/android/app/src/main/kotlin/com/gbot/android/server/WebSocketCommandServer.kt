package com.gbot.android.server

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.util.Log
import androidx.core.content.FileProvider
import com.google.gson.Gson
import com.google.gson.JsonObject
import com.google.gson.JsonSyntaxException
import com.gbot.android.model.CommandRequest
import com.gbot.android.model.CommandResponse
import com.gbot.android.service.MobileAccessibilityService
import org.java_websocket.WebSocket
import org.java_websocket.handshake.ClientHandshake
import org.java_websocket.server.WebSocketServer
import java.io.File
import java.io.FileOutputStream
import java.net.InetSocketAddress
import java.nio.ByteBuffer
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class WebSocketCommandServer(
    appContext: Context,
    port: Int,
    private val onLog: (String) -> Unit,
    private val onConnectionChange: (Int) -> Unit
) : WebSocketServer(InetSocketAddress(port)) {

    // applicationContext avoids leaking an Activity passed in by the caller.
    private val appContext = appContext.applicationContext

    companion object {
        private const val TAG = "WSCommandServer"
    }

    private val gson = Gson()
    private val executor = Executors.newFixedThreadPool(4)
    private val connectedClients = mutableSetOf<WebSocket>()

    // Per-connection in-flight file transfer. Server-owned (not a conn
    // attachment) so the test FakeWebSocket, which no-ops attachment ops, can
    // still exercise begin/end/binary without modification.
    private data class FileTransferSession(
        var output: FileOutputStream? = null,
        var path: String = "",
        var expectedSize: Long = 0L,
        var bytesWritten: Long = 0L
    )
    private val transfers = mutableMapOf<WebSocket, FileTransferSession>()
    private val transfersLock = Any()

    // Optional: authentication token
    var authToken: String? = null

    // Latch released by onStart (success) or onError (bind failure). startSync
    // blocks on it so the caller learns synchronously whether the socket bound.
    private val startLatch = CountDownLatch(1)
    @Volatile private var startError: Throwable? = null

    init {
        // Allow rebinding a port left in TIME_WAIT by a previous crashed instance.
        isReuseAddr = true
    }

    override fun onOpen(conn: WebSocket, handshake: ClientHandshake) {
        val remoteAddr = conn.remoteSocketAddress?.toString() ?: "unknown"
        Log.i(TAG, "Client connected: $remoteAddr")
        onLog("Client connected: $remoteAddr")

        // Check auth token if set
        if (authToken != null) {
            val providedToken = handshake.getFieldValue("Authorization")
            if (providedToken != "Bearer $authToken") {
                conn.close(4001, "Unauthorized")
                onLog("Client rejected (bad token): $remoteAddr")
                return
            }
        }

        synchronized(connectedClients) {
            connectedClients.add(conn)
        }
        onConnectionChange(connectedClients.size)
    }

    override fun onClose(conn: WebSocket, code: Int, reason: String, remote: Boolean) {
        val remoteAddr = conn.remoteSocketAddress?.toString() ?: "unknown"
        Log.i(TAG, "Client disconnected: $remoteAddr")
        onLog("Client disconnected: $remoteAddr")
        // A dropped connection mid-transfer must free its FileOutputStream.
        val session = synchronized(transfersLock) { transfers.remove(conn) }
        session?.output?.close()
        synchronized(connectedClients) {
            connectedClients.remove(conn)
        }
        onConnectionChange(connectedClients.size)
    }

    override fun onMessage(conn: WebSocket, message: String) {
        executor.submit {
            try {
                val request = gson.fromJson(message, CommandRequest::class.java)
                if (request.command == null) {
                    sendError(conn, null, "Missing 'command' field")
                    return@submit
                }

                // File transfer commands bypass the accessibility service — they
                // are file-system ops, not UI automation.
                when (request.command) {
                    "receive_file_begin" -> { handleFileBegin(conn, request); return@submit }
                    "receive_file_end" -> { handleFileEnd(conn, request); return@submit }
                }

                onLog(">> ${request.command}" +
                    if (request.params != null) " ${request.params}" else "")

                val service = MobileAccessibilityService.instance
                if (service == null) {
                    sendError(conn, request.id, "Accessibility service is not running. Enable it in Settings.")
                    return@submit
                }

                val response = service.handleCommand(request)
                // Stamp the request ID onto the response
                val finalResponse = response.copy(id = request.id)
                val json = gson.toJson(finalResponse)
                conn.send(json)

                val preview = if (json.length > 200) json.take(200) + "..." else json
                onLog("<< ${request.command}: ${if (finalResponse.success) "OK" else "ERR"}")

            } catch (e: JsonSyntaxException) {
                sendError(conn, null, "Invalid JSON: ${e.message}")
            } catch (e: Exception) {
                Log.e(TAG, "Error processing message", e)
                sendError(conn, null, "Internal error: ${e.message}")
            }
        }
    }

    override fun onError(conn: WebSocket?, ex: Exception) {
        Log.e(TAG, "WebSocket error", ex)
        onLog("Error: ${ex.message}")
        // Capture the first error and release the latch so startSync can fail.
        if (startLatch.count > 0) {
            startError = ex
            startLatch.countDown()
        }
    }

    override fun onStart() {
        Log.i(TAG, "WebSocket server started on port ${this.port}")
        onLog("Server started on port ${this.port}")
        connectionLostTimeout = 60
        startLatch.countDown()
    }

    /**
     * Starts the server synchronously: calls start() and blocks until onStart
     * (bind succeeded) or onError (bind failed), up to [timeoutMs]. Throws on
     * failure so the caller's try/catch can roll back UI state.
     */
    fun startSync(timeoutMs: Long = 3000L) {
        start()
        if (!startLatch.await(timeoutMs, TimeUnit.MILLISECONDS)) {
            throw java.net.BindException("Server start timed out after ${timeoutMs}ms")
        }
        startError?.let { throw it }
    }

    fun getConnectionCount(): Int = synchronized(connectedClients) { connectedClients.size }

    override fun onMessage(conn: WebSocket, bytes: ByteBuffer) {
        // Copy the ByteBuffer BEFORE entering the synchronized block: org.java_websocket
        // reuses/pools buffers, so we must materialize the bytes before the frame
        // returns. Doing the copy outside the lock keeps the critical section short.
        val copy = ByteArray(bytes.remaining())
        bytes.get(copy)

        // The write + counter mutation MUST be inside synchronized(transfersLock):
        // the library dispatches binary frames for ONE connection across MULTIPLE
        // executor threads (the server's thread pool), so two frames for the same
        // conn can run onMessage concurrently. Mutating session.output and
        // session.bytesWritten outside the lock would be a data race (torn writes,
        // lost counter increments). The session reference is also read under the
        // same lock so we don't observe a half-removed entry during onClose.
        synchronized(transfersLock) {
            val session = transfers[conn]
            if (session == null) {
                // Binary frame with no active transfer — drop silently (no ack
                // channel for binary frames). Log so a stray frame is diagnosable.
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

    private fun handleFileBegin(conn: WebSocket, request: CommandRequest) {
        val relPath = request.params?.get("path")?.asString
            ?: return sendError(conn, request.id, "receive_file_begin requires 'path'")
        val size = request.params.get("size")?.asLong ?: 0L
        // Sanitize: basename only, reject traversal. The model never sends a
        // device path (Go sends filepath.Base), so any '/' or '..' is malformed.
        val name = File(relPath).name
        if (name.isEmpty() || name.contains("..")) {
            return sendError(conn, request.id, "invalid path: $relPath")
        }
        val baseDir = appContext.getExternalFilesDir(null)
            ?: return sendError(conn, request.id, "external files dir unavailable")
        val target = File(baseDir, name)
        try {
            val out = FileOutputStream(target)
            synchronized(transfersLock) {
                transfers[conn] = FileTransferSession(
                    output = out, path = name, expectedSize = size, bytesWritten = 0L
                )
            }
        } catch (e: Exception) {
            return sendError(conn, request.id, "open output: ${e.message}")
        }
        onLog(">> receive_file_begin: $name ($size bytes)")
        conn.send(gson.toJson(CommandResponse.success(request.id, JsonObject().apply {
            addProperty("path", name)
        })))
    }

    private fun handleFileEnd(conn: WebSocket, request: CommandRequest) {
        val session = synchronized(transfersLock) { transfers.remove(conn) }
        val out = session?.output
        if (out == null) {
            return sendError(conn, request.id, "receive_file_end without an active transfer")
        }
        out.close()
        if (session.expectedSize > 0 && session.bytesWritten != session.expectedSize) {
            return sendError(conn, request.id,
                "size mismatch: wrote ${session.bytesWritten}, expected ${session.expectedSize}")
        }
        val file = File(appContext.getExternalFilesDir(null), session.path)
        val isApk = session.path.endsWith(".apk", ignoreCase = true)
        if (isApk) installApk(file)
        onLog("<< receive_file_end: ${session.path} (${session.bytesWritten} bytes)${if (isApk) " + install" else ""}")
        conn.send(gson.toJson(CommandResponse.success(request.id, JsonObject().apply {
            addProperty("bytes", session.bytesWritten)
            addProperty("installed", isApk)
        })))
    }

    private fun installApk(file: File) {
        val authority = "${appContext.packageName}.fileprovider"
        val uri = FileProvider.getUriForFile(appContext, authority, file)
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        appContext.startActivity(intent)
    }

    private fun sendError(conn: WebSocket, id: String?, message: String) {
        val response = CommandResponse.error(id, message)
        conn.send(gson.toJson(response))
        onLog("<< ERROR: $message")
    }

    fun shutdown() {
        try {
            executor.shutdownNow()
            stop(1000)
        } catch (e: Exception) {
            Log.e(TAG, "Error shutting down", e)
        }
    }
}
