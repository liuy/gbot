package com.gbot.android

import android.accessibilityservice.AccessibilityServiceInfo
import android.content.Context
import android.content.Intent
import android.graphics.drawable.GradientDrawable
import android.net.wifi.WifiManager
import android.os.Bundle
import android.provider.Settings
import android.view.accessibility.AccessibilityManager
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.gbot.android.databinding.ActivityMainBinding
import com.gbot.android.server.WebSocketCommandServer
import com.gbot.android.service.ConnectionForegroundService
import com.gbot.android.service.MobileAccessibilityService
import com.gbot.android.tunnel.SshTunnelConfig
import com.gbot.android.tunnel.SshTunnelState
import com.google.android.material.tabs.TabLayout
import java.text.SimpleDateFormat
import java.util.*

enum class Mode { WIFI, SSH }

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private var wsServer: WebSocketCommandServer? = null
    private var isServerRunning = false
    private var currentMode = Mode.WIFI
    private val dateFormat = SimpleDateFormat("HH:mm:ss", Locale.getDefault())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        setupUI()
        setupTabs()
        loadSshConfigToFields()
        updateAccessibilityStatus()
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
        binding.tabLayout.addTab(binding.tabLayout.newTab().setText(R.string.tab_wifi))
        binding.tabLayout.addTab(binding.tabLayout.newTab().setText(R.string.tab_ssh_tunnel))

        binding.tabLayout.addOnTabSelectedListener(object : TabLayout.OnTabSelectedListener {
            override fun onTabSelected(tab: TabLayout.Tab) {
                currentMode = if (tab.position == 1) Mode.SSH else Mode.WIFI
                applyModeVisibility()
                updateIPAddress()
            }

            override fun onTabUnselected(tab: TabLayout.Tab) {}
            override fun onTabReselected(tab: TabLayout.Tab) {}
        })

        applyModeVisibility()
    }

    private fun applyModeVisibility() {
        if (currentMode == Mode.SSH) {
            binding.wifiConfigGroup.visibility = android.view.View.GONE
            binding.sshConfigGroup.visibility = android.view.View.VISIBLE
        } else {
            binding.wifiConfigGroup.visibility = android.view.View.VISIBLE
            binding.sshConfigGroup.visibility = android.view.View.GONE
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
            wsServer = WebSocketCommandServer(
                port = port,
                onLog = { message -> runOnUiThread { appendLog(message) } },
                onConnectionChange = { count ->
                    runOnUiThread { binding.tvConnections.text = count.toString() }
                }
            )
            wsServer?.start()

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
            appendLog("Server started on port $port")

        } catch (e: Exception) {
            appendLog("Failed to start server: ${e.message}")
        }
    }

    private fun handleTunnelState(state: SshTunnelState) {
        when (state) {
            SshTunnelState.Connected -> {
                binding.tvStatus.text = getString(R.string.status_connected)
                setStatusColor(R.color.status_connected)
                appendLog(getString(R.string.ssh_tunnel_connected))
            }
            SshTunnelState.Connecting,
            is SshTunnelState.Reconnecting -> {
                binding.tvStatus.text = getString(R.string.status_waiting)
                setStatusColor(R.color.status_waiting)
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
            binding.tvStatus.text = getString(R.string.status_waiting)
            setStatusColor(R.color.status_waiting)
            if (currentMode == Mode.SSH) {
                binding.etPort.isEnabled = false
                binding.etServer.isEnabled = false
                binding.etSshPort.isEnabled = false
                binding.etSshUser.isEnabled = false
                binding.etSshPassword.isEnabled = false
                binding.etRemotePort.isEnabled = false
                binding.etLocalPort.isEnabled = false
            } else {
                binding.etPort.isEnabled = false
            }
        } else {
            binding.btnToggleServer.text = getString(R.string.btn_start_server)
            binding.tvStatus.text = getString(R.string.status_disconnected)
            setStatusColor(R.color.status_disconnected)
            binding.tvConnections.text = "0"
            binding.etPort.isEnabled = true
            binding.etServer.isEnabled = true
            binding.etSshPort.isEnabled = true
            binding.etSshUser.isEnabled = true
            binding.etSshPassword.isEnabled = true
            binding.etRemotePort.isEnabled = true
            binding.etLocalPort.isEnabled = true
        }
    }

    private fun updateAccessibilityStatus() {
        val isEnabled = isAccessibilityServiceEnabled()
        val indicator = binding.accessibilityIndicator.background as? GradientDrawable
            ?: GradientDrawable().also { binding.accessibilityIndicator.background = it }

        if (isEnabled) {
            indicator.setColor(ContextCompat.getColor(this, R.color.status_connected))
            binding.tvAccessibilityStatus.text = "Accessibility Service: ON"
        } else {
            indicator.setColor(ContextCompat.getColor(this, R.color.status_disconnected))
            binding.tvAccessibilityStatus.text = "Accessibility Service: OFF"
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

    private fun setStatusColor(colorRes: Int) {
        val color = ContextCompat.getColor(this, colorRes)
        val indicator = binding.statusIndicator.background as? GradientDrawable
            ?: GradientDrawable().also {
                it.shape = GradientDrawable.OVAL
                binding.statusIndicator.background = it
            }
        indicator.setColor(color)
    }

    private fun updateIPAddress() {
        if (currentMode == Mode.SSH) {
            val cfg = readSshConfigFromFields()
            binding.tvIpAddress.text = if (cfg.host.isNotBlank()) {
                "${cfg.host}:${cfg.remotePort}"
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

        // Auto-scroll
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
    }
}
