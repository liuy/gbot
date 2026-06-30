package com.gbot.android.service

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.IBinder
import androidx.core.app.NotificationCompat
import com.gbot.android.MainActivity
import com.gbot.android.R
import com.gbot.android.server.GbotWebSocketClient
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.net.URI
import kotlin.math.min

/**
 * ConnectionForegroundService owns the persistent outbound WebSocket to gbot.
 * It dials ws://host:port/ws on start, stays connected for the service
 * lifetime, and reconnects with exponential backoff (1→60s) on any drop. The
 * foreground notification keeps Android from killing the connection; the
 * actual command/response protocol lives in GbotWebSocketClient.
 *
 * Replaces the old SSH reverse tunnel: the persistence role the tunnel played
 * is now filled by this always-on WS client.
 */
class ConnectionForegroundService : Service() {

    companion object {
        const val CHANNEL_ID = "gbot_server_channel"
        const val NOTIFICATION_ID = 1001
        const val EXTRA_HOST = "extra_host" // gbot server host (was: SSH server host)
        const val EXTRA_PORT = "extra_port" // gbot server port, default 8765

        var logSink: ((String) -> Unit)? = null
        var connSink: ((Int) -> Unit)? = null // 1 connected, 0 disconnected
    }

    private var wsClient: GbotWebSocketClient? = null
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    @Volatile private var active = false
    private var loopJob: Job? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val host = intent?.getStringExtra(EXTRA_HOST) ?: ""
        val port = intent?.getIntExtra(EXTRA_PORT, 8765) ?: 8765

        val pendingIntent = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.notification_title))
            .setContentText(getString(R.string.notification_text, port))
            .setSmallIcon(android.R.drawable.ic_menu_share)
            .setContentIntent(pendingIntent)
            .setOngoing(true)
            .build()

        startForeground(NOTIFICATION_ID, notification)
        startClient(host, port)
        return START_STICKY
    }

    private fun startClient(host: String, port: Int) {
        if (active) return
        active = true
        loopJob = scope.launch { connectLoop(host, port) }
    }

    // connectLoop mirrors SshTunnelManager.connectLoop: dial, on success block
    // until the peer drops, on any failure back off and retry. backoff is
    // 1→2→4→…→60 (cap 60), reset to 1 on a successful connect.
    private suspend fun connectLoop(host: String, port: Int) {
        var backoff = 1
        while (active) {
            var client: GbotWebSocketClient? = null
            try {
                val uri = URI("ws://$host:$port/ws")
                client = GbotWebSocketClient(
                    uri,
                    onLog = { msg -> logSink?.invoke(msg) },
                    onConnectionChange = { count -> connSink?.invoke(count) }
                )
                client.setContext(applicationContext)
                wsClient = client
                // connectBlocking() returns Boolean: true = handshake open,
                // false = connect failed synchronously. The no-arg form is the
                // documented 1.5.6 API; a hung connect is bounded by the OS
                // TCP timeout and the loop eventually retries.
                val opened = client.connectBlocking()
                if (!opened) {
                    logSink?.invoke("Connect refused at $host:$port; retry in ${backoff}s")
                } else {
                    logSink?.invoke("Connected to gbot at $host:$port")
                    backoff = 1
                    // Block until the client disconnects. WebSocketClient has no
                    // built-in "await peer close", so a latch counted down in
                    // onClose drives it (GbotWebSocketClient.awaitClose).
                    client.awaitClose()
                }
            } catch (ce: CancellationException) {
                throw ce
            } catch (e: Exception) {
                logSink?.invoke("Connect failed: ${e.message}; retry in ${backoff}s")
            } finally {
                runCatching { client?.shutdown() }
            }
            if (!active) break
            delay(backoff * 1000L)
            backoff = min(60, backoff * 2)
        }
    }

    override fun onDestroy() {
        active = false
        loopJob?.cancel()
        wsClient?.shutdown()
        wsClient = null
        scope.cancel()
        super.onDestroy()
    }

    private fun createNotificationChannel() {
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.notification_channel_name),
            NotificationManager.IMPORTANCE_LOW
        ).apply {
            description = getString(R.string.notification_channel_description)
        }
        val notificationManager = getSystemService(NotificationManager::class.java)
        notificationManager.createNotificationChannel(channel)
    }
}
