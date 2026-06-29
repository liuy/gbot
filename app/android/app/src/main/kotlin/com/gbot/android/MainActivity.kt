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
import java.text.SimpleDateFormat
import java.util.*

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private var wsServer: WebSocketCommandServer? = null
    private var isServerRunning = false
    private val dateFormat = SimpleDateFormat("HH:mm:ss", Locale.getDefault())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        setupUI()
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

    private fun startServer() {
        if (!MobileAccessibilityService.isRunning) {
            appendLog("ERROR: Accessibility service is not enabled!")
            appendLog("Please enable 'GBot' in Accessibility Settings")
            return
        }

        val port = binding.etPort.text.toString().toIntOrNull() ?: 8765

        try {
            wsServer = WebSocketCommandServer(
                port = port,
                onLog = { message -> runOnUiThread { appendLog(message) } },
                onConnectionChange = { count ->
                    runOnUiThread { binding.tvConnections.text = count.toString() }
                }
            )
            wsServer?.start()

            // Start foreground service to keep alive
            val serviceIntent = Intent(this, ConnectionForegroundService::class.java).apply {
                putExtra(ConnectionForegroundService.EXTRA_PORT, port)
            }
            startForegroundService(serviceIntent)

            isServerRunning = true
            updateServerUI()
            appendLog("Server started on port $port")

        } catch (e: Exception) {
            appendLog("Failed to start server: ${e.message}")
        }
    }

    private fun stopServer() {
        try {
            wsServer?.shutdown()
            wsServer = null

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
            binding.etPort.isEnabled = false
        } else {
            binding.btnToggleServer.text = getString(R.string.btn_start_server)
            binding.tvStatus.text = getString(R.string.status_disconnected)
            setStatusColor(R.color.status_disconnected)
            binding.tvConnections.text = "0"
            binding.etPort.isEnabled = true
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
