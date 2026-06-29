package com.gbot.android.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.IBinder
import androidx.core.app.NotificationCompat
import com.gbot.android.MainActivity
import com.gbot.android.R
import com.gbot.android.tunnel.SshTunnelConfig
import com.gbot.android.tunnel.SshTunnelManager
import com.gbot.android.tunnel.SshTunnelState
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob

class ConnectionForegroundService : Service() {

    companion object {
        const val CHANNEL_ID = "gbot_server_channel"
        const val NOTIFICATION_ID = 1001
        const val EXTRA_PORT = "extra_port"
        const val EXTRA_USE_SSH = "extra_use_ssh"

        var logSink: ((String) -> Unit)? = null
        var stateSink: ((SshTunnelState) -> Unit)? = null
    }

    private var tunnelManager: SshTunnelManager? = null
    private val tunnelScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
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

        if (intent?.getBooleanExtra(EXTRA_USE_SSH, false) == true) {
            startTunnel()
        }

        return START_STICKY
    }

    private fun startTunnel() {
        val cfg = SshTunnelConfig.load(this)
        tunnelManager = SshTunnelManager(
            cfg = cfg,
            scope = tunnelScope,
            onLog = { msg -> logSink?.invoke(msg) },
            onState = { state -> stateSink?.invoke(state) }
        )
        tunnelManager?.start()
    }

    override fun onDestroy() {
        tunnelManager?.stop()
        tunnelManager = null
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
