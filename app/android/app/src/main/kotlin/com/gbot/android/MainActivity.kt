package com.gbot.android

import android.accessibilityservice.AccessibilityServiceInfo
import android.animation.ObjectAnimator
import android.content.Context
import android.content.Intent
import android.graphics.drawable.GradientDrawable
import android.os.Bundle
import android.provider.Settings
import android.view.View
import android.view.accessibility.AccessibilityManager
import android.view.animation.AccelerateDecelerateInterpolator
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.gbot.android.databinding.ActivityMainBinding
import com.gbot.android.service.ConnectionForegroundService
import com.gbot.android.service.MobileAccessibilityService
import java.text.SimpleDateFormat
import java.util.*

enum class StatusState(val textResId: Int, val colorResId: Int) {
    CONNECTED(R.string.status_connected, R.color.status_green),
    WAITING(R.string.status_waiting, R.color.status_orange),
    DISCONNECTED(R.string.status_disconnected, R.color.status_red)
}

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private var isServerRunning = false
    private val dateFormat = SimpleDateFormat("HH:mm:ss", Locale.getDefault())
    private var pulseAnimator: ObjectAnimator? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        setupUI()
        updateAccessibilityStatus()
        updateStatus(StatusState.DISCONNECTED)
    }

    override fun onResume() {
        super.onResume()
        updateAccessibilityStatus()
    }

    private fun setupUI() {
        binding.btnToggleServer.setOnClickListener {
            if (isServerRunning) stopServer() else startServer()
        }

        binding.btnOpenAccessibility.setOnClickListener {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        }
    }

    private fun startServer() {
        if (!MobileAccessibilityService.isRunning) {
            appendLog("ERROR: Accessibility service is not enabled!")
            appendLog("Please enable 'GBot' in Accessibility Settings")
            return
        }

        val host = binding.etServer.text.toString().trim()
        if (host.isEmpty()) {
            appendLog("ERROR: Enter the gbot server host")
            return
        }
        val port = binding.etPort.text.toString().toIntOrNull() ?: 8765

        ConnectionForegroundService.logSink = { msg ->
            runOnUiThread { appendLog(msg) }
        }
        ConnectionForegroundService.connSink = { count ->
            runOnUiThread {
                binding.tvConnections.text = count.toString()
                if (count > 0 && isServerRunning) {
                    updateStatus(StatusState.CONNECTED)
                } else if (isServerRunning) {
                    updateStatus(StatusState.WAITING)
                }
            }
        }

        val serviceIntent = Intent(this, ConnectionForegroundService::class.java).apply {
            putExtra(ConnectionForegroundService.EXTRA_HOST, host)
            putExtra(ConnectionForegroundService.EXTRA_PORT, port)
        }
        startForegroundService(serviceIntent)

        isServerRunning = true
        updateServerUI()
    }

    private fun stopServer() {
        try {
            ConnectionForegroundService.logSink = null
            ConnectionForegroundService.connSink = null
            stopService(Intent(this, ConnectionForegroundService::class.java))

            isServerRunning = false
            updateServerUI()
            appendLog("Disconnected")
        } catch (e: Exception) {
            appendLog("Error disconnecting: ${e.message}")
        }
    }

    private fun updateServerUI() {
        if (isServerRunning) {
            binding.btnToggleServer.text = getString(R.string.btn_disconnect)
            updateStatus(StatusState.WAITING)
            binding.serverConfigGroup.visibility = View.GONE
        } else {
            binding.btnToggleServer.text = getString(R.string.btn_connect)
            updateStatus(StatusState.DISCONNECTED)
            binding.tvConnections.text = "0"
            binding.serverConfigGroup.visibility = View.VISIBLE
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
