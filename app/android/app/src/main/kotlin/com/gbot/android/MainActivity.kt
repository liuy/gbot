package com.gbot.android

import android.accessibilityservice.AccessibilityServiceInfo
import android.animation.ObjectAnimator
import android.content.Context
import android.content.Intent
import android.graphics.drawable.GradientDrawable
import android.net.wifi.WifiManager
import android.os.Bundle
import android.provider.Settings
import android.view.View
import android.widget.TextView
import android.view.accessibility.AccessibilityManager
import android.view.animation.AccelerateDecelerateInterpolator
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.gbot.android.databinding.ActivityMainBinding
import com.gbot.android.server.WebSocketCommandServer
import com.gbot.android.service.ConnectionForegroundService
import com.gbot.android.service.MobileAccessibilityService
import com.gbot.android.tunnel.SshTunnelConfig
import com.gbot.android.tunnel.SshTunnelState
import java.text.SimpleDateFormat
import java.util.*

enum class Mode { WIFI, SSH }

enum class StatusState(val textResId: Int, val colorResId: Int) {
    CONNECTED(R.string.status_connected, R.color.status_green),
    WAITING(R.string.status_waiting, R.color.status_orange),
    DISCONNECTED(R.string.status_disconnected, R.color.status_red)
}

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private var wsServer: WebSocketCommandServer? = null
    private var isServerRunning = false
    private var currentMode = Mode.WIFI
    private val dateFormat = SimpleDateFormat("HH:mm:ss", Locale.getDefault())
    private var pulseAnimator: ObjectAnimator? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        setupUI()
        setupTabs()
        loadSshConfigToFields()
        updateAccessibilityStatus()
        updateStatus(StatusState.DISCONNECTED)
    }

    override fun onResume() {
        super.onResume()
        updateAccessibilityStatus()
        updateIPAddress()
    }

    private fun setupUI() {
        binding.btnToggleServer.setOnClickListener {
            if (isServerRunning) stopServer() else startServer()
        }

        binding.btnOpenAccessibility.setOnClickListener {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        }

        updateIPAddress()
    }

    private fun setupTabs() {
        fun selectTab(selected: TextView, deselected: TextView) {
            selected.isSelected = true
            selected.setTextColor(getColor(R.color.text_value))
            deselected.isSelected = false
            deselected.setTextColor(getColor(R.color.text_label))
        }

        binding.tabWifi.setOnClickListener {
            selectTab(binding.tabWifi, binding.tabSsh)
            currentMode = Mode.WIFI
            applyModeVisibility()
            updateIPAddress()
        }

        binding.tabSsh.setOnClickListener {
            selectTab(binding.tabSsh, binding.tabWifi)
            currentMode = Mode.SSH
            applyModeVisibility()
            updateIPAddress()
        }

        // Default to WiFi
        selectTab(binding.tabWifi, binding.tabSsh)
        applyModeVisibility()
    }

    private fun applyModeVisibility() {
        // Server running: both config groups stay hidden (set in updateServerUI).
        if (isServerRunning) {
            binding.wifiConfigGroup.visibility = View.GONE
            binding.sshConfigGroup.visibility = View.GONE
            return
        }
        if (currentMode == Mode.SSH) {
            binding.wifiConfigGroup.visibility = View.GONE
            binding.sshConfigGroup.visibility = View.VISIBLE
        } else {
            binding.wifiConfigGroup.visibility = View.VISIBLE
            binding.sshConfigGroup.visibility = View.GONE
        }
    }

    private fun loadSshConfigToFields() {
        val cfg = SshTunnelConfig.load(this)
        binding.etServer.setText(cfg.host)
        binding.etSshPort.setText(cfg.port.toString())
        binding.etSshUser.setText(cfg.user)
        binding.etSshPassword.setText(cfg.password)
        binding.etRemotePort.setText(cfg.remotePort.toString())
        binding.etLocalPort.setText(cfg.localPort.toString())

        val saveOnLeave = android.view.View.OnFocusChangeListener { _, hasFocus ->
            if (!hasFocus) saveSshConfigFromFields()
        }
        binding.etServer.onFocusChangeListener = saveOnLeave
        binding.etSshPort.onFocusChangeListener = saveOnLeave
        binding.etSshUser.onFocusChangeListener = saveOnLeave
        binding.etSshPassword.onFocusChangeListener = saveOnLeave
        binding.etRemotePort.onFocusChangeListener = saveOnLeave
        binding.etLocalPort.onFocusChangeListener = saveOnLeave
    }

    private fun saveSshConfigFromFields() {
        SshTunnelConfig.save(this, readSshConfigFromFields())
    }

    private fun readSshConfigFromFields(): SshTunnelConfig {
        return SshTunnelConfig(
            host = binding.etServer.text.toString().trim(),
            port = binding.etSshPort.text.toString().toIntOrNull() ?: 22,
            user = binding.etSshUser.text.toString().trim(),
            password = binding.etSshPassword.text.toString(),
            remotePort = binding.etRemotePort.text.toString().toIntOrNull() ?: 8765,
            localPort = binding.etLocalPort.text.toString().toIntOrNull() ?: 8765
        )
    }

    private fun startServer() {
        if (!MobileAccessibilityService.isRunning) {
            appendLog("ERROR: Accessibility service is not enabled!")
            appendLog("Please enable 'GBot' in Accessibility Settings")
            return
        }

        val useSsh = currentMode == Mode.SSH
        val port: Int

        if (useSsh) {
            val cfg = readSshConfigFromFields()
            if (!cfg.isValid()) {
                appendLog(getString(R.string.ssh_invalid_config))
                return
            }
            saveSshConfigFromFields()
            port = cfg.localPort
        } else {
            port = binding.etPort.text.toString().toIntOrNull() ?: 8765
        }

        try {
            // Clear any leftover server instance (crash recovery / stopped-but-not-niled).
            wsServer?.shutdown()

            wsServer = WebSocketCommandServer(
                port = port,
                onLog = { message -> runOnUiThread { appendLog(message) } },
                onConnectionChange = { count ->
                    runOnUiThread {
                        binding.tvConnections.text = count.toString()
                        if (count > 0 && isServerRunning) {
                            updateStatus(StatusState.CONNECTED)
                        } else if (isServerRunning) {
                            updateStatus(StatusState.WAITING)
                        }
                    }
                }
            )
            wsServer?.startSync()

            ConnectionForegroundService.logSink = { msg ->
                runOnUiThread { appendLog(msg) }
            }
            ConnectionForegroundService.stateSink = { state ->
                runOnUiThread { handleTunnelState(state) }
            }

            val serviceIntent = Intent(this, ConnectionForegroundService::class.java).apply {
                putExtra(ConnectionForegroundService.EXTRA_PORT, port)
                putExtra(ConnectionForegroundService.EXTRA_USE_SSH, useSsh)
            }
            startForegroundService(serviceIntent)

            isServerRunning = true
            updateServerUI()
            if (useSsh) {
                appendLog(getString(R.string.ssh_tunnel_starting))
            }

        } catch (e: Exception) {
            wsServer?.shutdown()
            wsServer = null
            appendLog("Failed to start server: ${e.message}")
        }
    }

    private fun handleTunnelState(state: SshTunnelState) {
        when (state) {
            SshTunnelState.Connected -> {
                appendLog(getString(R.string.ssh_tunnel_connected))
                if (binding.tvConnections.text.toString().toIntOrNull() == 0) {
                    updateStatus(StatusState.WAITING)
                }
            }
            SshTunnelState.Connecting,
            is SshTunnelState.Reconnecting -> {
                updateStatus(StatusState.WAITING)
            }
            SshTunnelState.Stopped -> {
                appendLog(getString(R.string.ssh_tunnel_disconnected))
            }
            SshTunnelState.Idle -> {}
        }
    }

    private fun stopServer() {
        try {
            wsServer?.shutdown()
            wsServer = null

            ConnectionForegroundService.logSink = null
            ConnectionForegroundService.stateSink = null

            stopService(Intent(this, ConnectionForegroundService::class.java))

            isServerRunning = false
            updateServerUI()
            appendLog("Server stopped")

        } catch (e: Exception) {
            appendLog("Error stopping server: ${e.message}")
        }
    }

    private fun updateServerUI() {
        if (isServerRunning) {
            binding.btnToggleServer.text = getString(R.string.btn_stop_server)
            updateStatus(StatusState.WAITING)
            binding.wifiConfigGroup.visibility = View.GONE
            binding.sshConfigGroup.visibility = View.GONE
        } else {
            binding.btnToggleServer.text = getString(R.string.btn_start_server)
            updateStatus(StatusState.DISCONNECTED)
            binding.tvConnections.text = "0"
            applyModeVisibility()
        }
    }

    private fun updateAccessibilityStatus() {
        val isEnabled = isAccessibilityServiceEnabled()
        val indicator = binding.accessibilityIndicator.background as? GradientDrawable
            ?: GradientDrawable().also { binding.accessibilityIndicator.background = it }

        if (isEnabled) {
            indicator.setColor(ContextCompat.getColor(this, R.color.status_connected))
            binding.tvAccessibilityStatus.text = getString(R.string.accessibility_status_on)
            binding.btnOpenAccessibility.visibility = View.GONE
        } else {
            indicator.setColor(ContextCompat.getColor(this, R.color.status_disconnected))
            binding.tvAccessibilityStatus.text = getString(R.string.accessibility_status_off)
            binding.btnOpenAccessibility.visibility = View.VISIBLE
        }
    }

    private fun isAccessibilityServiceEnabled(): Boolean {
        val am = getSystemService(Context.ACCESSIBILITY_SERVICE) as AccessibilityManager
        val enabledServices = am.getEnabledAccessibilityServiceList(
            AccessibilityServiceInfo.FEEDBACK_GENERIC
        )
        return enabledServices.any {
            it.resolveInfo.serviceInfo.packageName == packageName
        }
    }

    private fun updateStatus(state: StatusState) {
        binding.tvStatus.text = getString(state.textResId)
        val color = ContextCompat.getColor(this, state.colorResId)
        val indicator = binding.statusIndicator.background as? GradientDrawable
            ?: GradientDrawable().also {
                it.shape = GradientDrawable.OVAL
                binding.statusIndicator.background = it
            }
        indicator.setColor(color)
        if (state == StatusState.CONNECTED || state == StatusState.WAITING) {
            startPulse()
        } else {
            stopPulse()
        }
    }

    private fun startPulse() {
        if (pulseAnimator?.isRunning == true) return
        pulseAnimator = ObjectAnimator.ofFloat(
            binding.statusIndicator, View.ALPHA, 1.0f, 0.4f
        ).apply {
            duration = 1200
            repeatMode = ObjectAnimator.REVERSE
            repeatCount = ObjectAnimator.INFINITE
            interpolator = AccelerateDecelerateInterpolator()
            start()
        }
    }

    private fun stopPulse() {
        pulseAnimator?.cancel()
        pulseAnimator = null
        binding.statusIndicator.alpha = 1.0f
    }

    private fun updateIPAddress() {
        if (currentMode == Mode.SSH) {
            val cfg = readSshConfigFromFields()
            binding.tvIpAddress.text = if (cfg.host.isNotBlank()) {
                cfg.host
            } else {
                getString(R.string.status_disconnected)
            }
            return
        }
        val ip = getDeviceIpAddress()
        binding.tvIpAddress.text = ip ?: "Not connected to WiFi"
    }

    private fun getDeviceIpAddress(): String? {
        try {
            val wifiManager = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
            val wifiInfo = wifiManager.connectionInfo
            val ip = wifiInfo.ipAddress
            if (ip == 0) return null
            return String.format(
                "%d.%d.%d.%d",
                ip and 0xff, ip shr 8 and 0xff,
                ip shr 16 and 0xff, ip shr 24 and 0xff
            )
        } catch (e: Exception) {
            return null
        }
    }

    private fun appendLog(message: String) {
        val timestamp = dateFormat.format(Date())
        val logLine = "[$timestamp] $message\n"
        binding.tvLog.append(logLine)

        // Auto-scroll the log's own container, not the outer mainScrollView —
        // the log lives inside its own NestedScrollView so it scrolls independently.
        binding.logScrollView.post {
            binding.logScrollView.fullScroll(android.view.View.FOCUS_DOWN)
        }

        // Keep log size reasonable
        val text = binding.tvLog.text.toString()
        if (text.length > 10000) {
            binding.tvLog.text = text.substring(text.length - 5000)
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        if (isServerRunning) {
            stopServer()
        }
        stopPulse()
    }
}
