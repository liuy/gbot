package com.gbot.android.tunnel

import com.jcraft.jsch.JSch
import com.jcraft.jsch.Session
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlin.math.min

sealed interface SshTunnelState {
    object Idle : SshTunnelState
    object Connecting : SshTunnelState
    object Connected : SshTunnelState
    data class Reconnecting(val waitSeconds: Int) : SshTunnelState
    object Stopped : SshTunnelState
}

class SshTunnelManager(
    private val cfg: SshTunnelConfig,
    private val scope: CoroutineScope,
    private val onLog: (String) -> Unit,
    private val onState: (SshTunnelState) -> Unit
) {
    @Volatile
    private var active = false
    private var loopJob: Job? = null

    fun start() {
        if (active) return
        active = true
        loopJob = scope.launch { connectLoop() }
    }

    fun stop() {
        active = false
        loopJob?.cancel()
        loopJob = null
        onState(SshTunnelState.Stopped)
    }

    private suspend fun connectLoop() {
        var backoff = 1
        while (active) {
            var session: Session? = null
            try {
                onState(SshTunnelState.Connecting)
                // JSch sessions are not reusable after disconnect, so create fresh per attempt.
                val jsch = JSch()
                session = jsch.getSession(cfg.user, cfg.host, cfg.port)
                session.setPassword(cfg.password)
                session.setConfig("StrictHostKeyChecking", "no")
                session.serverAliveInterval = 30_000
                session.serverAliveCountMax = 3
                session.connect(10_000)
                session.setPortForwardingR(cfg.remotePort, "127.0.0.1", cfg.localPort)
                onState(SshTunnelState.Connected)
                onLog("SSH tunnel established")
                backoff = 1
                while (active && session.isConnected) delay(2000)
            } catch (ce: CancellationException) {
                throw ce
            } catch (e: Exception) {
                onLog("SSH error: ${e.message}")
            } finally {
                // Cancellation or error must still release the socket to avoid
                // a half-open session lingering.
                runCatching { session?.disconnect() }
            }
            if (!active) break
            onState(SshTunnelState.Reconnecting(backoff))
            onLog("Reconnect in ${backoff}s")
            delay(backoff * 1000L)
            backoff = min(60, backoff * 2)
        }
    }
}
