package com.gbot.android.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.IBinder
import androidx.core.app.NotificationCompat
import com.gbot.android.ArtifactDownloader
import com.gbot.android.GbotProcess
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
import java.net.URISyntaxException
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
        const val DEFAULT_HOST = "127.0.0.1"
        const val DEFAULT_PORT = 8765

        // Companion-level so the fallback logic stays unit-testable without
        // Robolectric (its native binder does not load on arm64 Termux JVMs).
        internal fun resolveTarget(intent: Intent?): Pair<String, Int> = Pair(
            intent?.getStringExtra(EXTRA_HOST) ?: DEFAULT_HOST,
            intent?.getIntExtra(EXTRA_PORT, DEFAULT_PORT) ?: DEFAULT_PORT,
        )

        /**
         * Events URI for the loop to dial. Origin-form hosts (https://machine.ts.net)
         * carry their own scheme and port: the event stream rides wss/ws alongside
         * the page, and the port extra is ignored; bare hosts keep the legacy
         * ws://host:port form. Trailing slashes are trimmed so an origin saved as
         * "https://host/" cannot grow a "//ws" path. Null when the built URI does
         * not parse — the caller backs off and retries instead of crashing.
         */
        internal fun buildEventsUri(host: String, port: Int): URI? = try {
            when {
                host.startsWith("https://") ->
                    URI("wss://" + host.removePrefix("https://").trimEnd('/') + "/ws")
                host.startsWith("http://") ->
                    URI("ws://" + host.removePrefix("http://").trimEnd('/') + "/ws")
                else -> URI("ws://$host:$port/ws")
            }
        } catch (_: URISyntaxException) {
            null
        }

        // Origin-form hosts carry their port inside the string, so appending
        // the port extra there would be noise — logs show them verbatim.
        internal fun targetLabel(host: String, port: Int): String =
            if (host.contains("://")) host else "$host:$port"
    }

    // Written on the connect loop (IO), read by onDestroy (main) — same
    // visibility contract as the active/target fields beside it.
    @Volatile private var wsClient: GbotWebSocketClient? = null
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    @Volatile private var active = false
    private var loopJob: Job? = null
    // Last target the connection loop was started against — the "Connected to
    // gbot on port %d" content is synthesized from this, so the download
    // progress renderer can rebuild the idle form without new plumbing.
    @Volatile private var targetHost = DEFAULT_HOST
    @Volatile private var targetPort = DEFAULT_PORT

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
        // The downloader pokes this (throttled) whenever its state changed;
        // rendering stays service-owned: the content is re-synthesized here
        // from both states, so two writers can never fight over id 1001.
        ArtifactDownloader.requestRender = { renderStatusNotification() }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val (host, port) = resolveTarget(intent)
        targetHost = host
        targetPort = port

        startForeground(NOTIFICATION_ID, buildStatusNotification(host, port, ArtifactDownloader.state.get()))
        startClient(host, port)
        return START_STICKY
    }

    // THE single source of id-1001 content: both the startForeground post and
    // download-progress re-renders build through here, so a progress update
    // can never clobber the connection text maintained elsewhere.
    internal fun buildStatusNotification(host: String, port: Int, download: ArtifactDownloader.DownloadState): Notification {
        val builder = NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_menu_share)
            .setOngoing(true)
            .setContentIntent(
                PendingIntent.getActivity(
                    this,
                    0,
                    Intent(this, MainActivity::class.java),
                    PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
                )
            )
        // Origin-form hosts carry their port inside the string — "on port %d"
        // would name a port the connection does not use.
        val contentText = if (host.contains("://")) {
            getString(R.string.notification_text_origin, host)
        } else {
            getString(R.string.notification_text, port)
        }
        val downloading = download as? ArtifactDownloader.DownloadState.Downloading
        if (downloading != null) {
            builder.setContentTitle(getString(R.string.notification_downloading, downloading.name))
            builder.setContentText(contentText)
            // setProgress takes ints: clamp so artifacts over 2 GB degrade to
            // a saturated bar instead of throwing, and an unknown total (-1)
            // clamps to 0 — the bar then renders indeterminate, not negative.
            builder.setProgress(
                downloading.bytesTotal.coerceIn(0, Int.MAX_VALUE.toLong()).toInt(),
                downloading.bytesDone.coerceIn(0, Int.MAX_VALUE.toLong()).toInt(),
                downloading.bytesTotal <= 0,
            )
        } else {
            builder.setContentTitle(getString(R.string.notification_title))
            builder.setContentText(contentText)
        }
        return builder.build()
    }

    // internal: the render tests drive the service's real update path.
    internal fun renderStatusNotification() {
        getSystemService(NotificationManager::class.java)
            .notify(NOTIFICATION_ID, buildStatusNotification(targetHost, targetPort, ArtifactDownloader.state.get()))
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
                // A full-origin host (https://machine.ts.net) carries its own
                // scheme and port; bare hosts keep ws://host:port.
                val uri = buildEventsUri(host, port)
                    ?: throw IllegalStateException("unparsable target $host")
                // Assign the OUTER client — a local val here shadows it and
                // leaves the finally-block's shutdown() on a perpetual null
                // (leaked socket on every failed dial).
                client = GbotWebSocketClient(
                    uri,
                    // Event lines go straight into the app log buffer — the
                    // old Control tab sink is gone, the WUI panel reads the
                    // buffer directly.
                    onLog = { msg -> GbotProcess.appendEvent(msg) },
                    // Connection changes were a Control-tab status indicator;
                    // the drop is already logged via onLog in onClose.
                    onConnectionChange = { }
                )
                client.setContext(applicationContext)
                wsClient = client
                // connectBlocking() returns Boolean: true = handshake open,
                // false = connect failed synchronously. The no-arg form is the
                // documented 1.5.6 API; a hung connect is bounded by the OS
                // TCP timeout and the loop eventually retries.
                val opened = client.connectBlocking()
                if (!opened) {
                    GbotProcess.appendEvent("Connect failed at ${targetLabel(host, port)}; retry in ${backoff}s")
                } else {
                    backoff = 1
                    // Block until the client disconnects. WebSocketClient has no
                    // built-in "await peer close", so a latch counted down in
                    // onClose drives it (GbotWebSocketClient.awaitClose).
                    client.awaitClose()
                }
            } catch (ce: CancellationException) {
                throw ce
            } catch (e: Exception) {
                GbotProcess.appendEvent("Connect failed: ${e.message}; retry in ${backoff}s")
            } finally {
                runCatching { client?.shutdown() }
            }
            if (!active) break
            delay(backoff * 1000L)
            backoff = min(60, backoff * 2)
        }
    }

    override fun onDestroy() {
        // The hook captures this service — dropping it here keeps a destroyed
        // instance from rendering into a later test's or process's shadows.
        ArtifactDownloader.requestRender = null
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
