package com.gbot.android.server

import android.util.Log
import com.google.gson.Gson
import com.google.gson.JsonSyntaxException
import com.gbot.android.model.CommandRequest
import com.gbot.android.model.CommandResponse
import com.gbot.android.service.MobileAccessibilityService
import org.java_websocket.WebSocket
import org.java_websocket.handshake.ClientHandshake
import org.java_websocket.server.WebSocketServer
import java.net.InetSocketAddress
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class WebSocketCommandServer(
    port: Int,
    private val onLog: (String) -> Unit,
    private val onConnectionChange: (Int) -> Unit
) : WebSocketServer(InetSocketAddress(port)) {

    companion object {
        private const val TAG = "WSCommandServer"
    }

    private val gson = Gson()
    private val executor = Executors.newFixedThreadPool(4)
    private val connectedClients = mutableSetOf<WebSocket>()

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
