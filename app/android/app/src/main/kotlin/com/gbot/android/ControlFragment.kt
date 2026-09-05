package com.gbot.android

import android.accessibilityservice.AccessibilityServiceInfo
import android.animation.ObjectAnimator
import android.content.Context
import android.content.Intent
import android.graphics.drawable.GradientDrawable
import android.os.Bundle
import android.provider.Settings
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityManager
import android.view.animation.AccelerateDecelerateInterpolator
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.content.ContextCompat
import androidx.core.widget.NestedScrollView
import androidx.fragment.app.Fragment
import androidx.preference.PreferenceManager
import com.gbot.android.service.ConnectionForegroundService
import com.gbot.android.service.MobileAccessibilityService
import java.text.SimpleDateFormat
import java.util.*

class ControlFragment : Fragment() {

    private var statusIndicator: View? = null
    private var tvStatus: TextView? = null
    private var tvIpAddress: TextView? = null
    private var serverConfigGroup: LinearLayout? = null
    private var etServer: EditText? = null
    private var etPort: EditText? = null
    private var accessibilityIndicator: View? = null
    private var tvAccessibilityStatus: TextView? = null
    private var btnOpenAccessibility: Button? = null
    private var btnToggleServer: Button? = null
    private var tvLog: TextView? = null
    private var logScrollView: NestedScrollView? = null

    private var isServerRunning = false
    private val dateFormat = SimpleDateFormat("HH:mm:ss", Locale.getDefault())
    private var pulseAnimator: ObjectAnimator? = null

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?
    ): View {
        return inflater.inflate(R.layout.fragment_control, container, false)
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        statusIndicator = view.findViewById(R.id.statusIndicator)
        tvStatus = view.findViewById(R.id.tvStatus)
        tvIpAddress = view.findViewById(R.id.tvIpAddress)
        serverConfigGroup = view.findViewById(R.id.serverConfigGroup)
        etServer = view.findViewById(R.id.etServer)
        etPort = view.findViewById(R.id.etPort)
        accessibilityIndicator = view.findViewById(R.id.accessibilityIndicator)
        tvAccessibilityStatus = view.findViewById(R.id.tvAccessibilityStatus)
        btnOpenAccessibility = view.findViewById(R.id.btnOpenAccessibility)
        btnToggleServer = view.findViewById(R.id.btnToggleServer)
        tvLog = view.findViewById(R.id.tvLog)
        logScrollView = view.findViewById(R.id.logScrollView)

        setupUI()
        updateAccessibilityStatus()
        updateStatus(StatusState.DISCONNECTED)

        val prefs = PreferenceManager.getDefaultSharedPreferences(requireContext())
        if (prefs.getString("host", null) == null) {
            prefs.edit().putString("host", "10.0.2.2").putString("port", "8765").apply()
        }

        // Edit Config button
        view.findViewById<android.widget.Button?>(R.id.btnEditConfig)?.setOnClickListener {
            showConfigEditor()
        }

        // Display gbot startup logs from the MainActivity launch
        val logs = GbotProcess.logBuffer.toString().trim()
        if (logs.isNotEmpty()) {
            logs.lines().forEach { appendLog(it) }
        }
    }

    override fun onResume() {
        super.onResume()
        updateAccessibilityStatus()
    }

    private fun setupUI() {
        btnToggleServer?.setOnClickListener {
            if (isServerRunning) stopServer() else startServer()
        }

        btnOpenAccessibility?.setOnClickListener {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        }

        // Long-press log area to copy all logs to clipboard
        tvLog?.setOnLongClickListener {
            val text = tvLog?.text?.toString() ?: ""
            val clipboard = requireContext().getSystemService(Context.CLIPBOARD_SERVICE) as android.content.ClipboardManager
            clipboard.setPrimaryClip(android.content.ClipData.newPlainText("gbot log", text))
            android.widget.Toast.makeText(requireContext(), "Log copied", android.widget.Toast.LENGTH_SHORT).show()
            true
        }
    }

    private fun startServer() {
        if (!MobileAccessibilityService.isRunning) {
            appendLog("ERROR: Accessibility service is not enabled!")
            appendLog("Please enable 'GBot' in Accessibility Settings")
            return
        }

        val host = etServer?.text?.toString()?.trim() ?: ""
        if (host.isEmpty()) {
            appendLog("ERROR: Enter the gbot server host")
            return
        }
        val port = etPort?.text?.toString()?.toIntOrNull() ?: 8765

        PreferenceManager.getDefaultSharedPreferences(requireContext())
            .edit()
            .putString("host", host)
            .putString("port", port.toString())
            .apply()

        ConnectionForegroundService.logSink = { msg ->
            requireActivity().runOnUiThread { appendLog(msg) }
        }
        ConnectionForegroundService.connSink = { connected, hostPort ->
            requireActivity().runOnUiThread {
                if (connected > 0 && isServerRunning) {
                    tvIpAddress?.text = hostPort
                    updateStatus(StatusState.CONNECTED)
                } else if (isServerRunning) {
                    updateStatus(StatusState.WAITING)
                }
            }
        }

        val serviceIntent = Intent(requireContext(), ConnectionForegroundService::class.java).apply {
            putExtra(ConnectionForegroundService.EXTRA_HOST, host)
            putExtra(ConnectionForegroundService.EXTRA_PORT, port)
        }
        requireContext().startForegroundService(serviceIntent)

        isServerRunning = true
        updateServerUI()
    }

    private fun stopServer() {
        try {
            ConnectionForegroundService.logSink = null
            ConnectionForegroundService.connSink = null
            requireContext().stopService(Intent(requireContext(), ConnectionForegroundService::class.java))

            isServerRunning = false
            updateServerUI()
            appendLog("Disconnected")
        } catch (e: Exception) {
            appendLog("Error disconnecting: ${e.message}")
        }
    }

    private fun updateServerUI() {
        if (isServerRunning) {
            btnToggleServer?.text = getString(R.string.btn_disconnect)
            updateStatus(StatusState.WAITING)
            serverConfigGroup?.visibility = View.GONE
        } else {
            btnToggleServer?.text = getString(R.string.btn_connect)
            updateStatus(StatusState.DISCONNECTED)
            tvIpAddress?.text = getString(R.string.placeholder_ip)
            serverConfigGroup?.visibility = View.VISIBLE
        }
    }

    private fun updateAccessibilityStatus() {
        val isEnabled = isAccessibilityServiceEnabled()
        val indicator = accessibilityIndicator?.background as? GradientDrawable
            ?: GradientDrawable().also { accessibilityIndicator?.background = it }

        if (isEnabled) {
            indicator.setColor(ContextCompat.getColor(requireContext(), R.color.status_connected))
            tvAccessibilityStatus?.text = getString(R.string.accessibility_status_on)
            btnOpenAccessibility?.visibility = View.GONE
        } else {
            indicator.setColor(ContextCompat.getColor(requireContext(), R.color.status_disconnected))
            tvAccessibilityStatus?.text = getString(R.string.accessibility_status_off)
            btnOpenAccessibility?.visibility = View.VISIBLE
        }
    }

    private fun isAccessibilityServiceEnabled(): Boolean {
        val am = requireContext().getSystemService(Context.ACCESSIBILITY_SERVICE) as AccessibilityManager
        val enabledServices = am.getEnabledAccessibilityServiceList(
            AccessibilityServiceInfo.FEEDBACK_GENERIC
        )
        return enabledServices.any {
            it.resolveInfo.serviceInfo.packageName == requireContext().packageName
        }
    }

    private fun updateStatus(state: StatusState) {
        tvStatus?.text = getString(state.textResId)
        val color = ContextCompat.getColor(requireContext(), state.colorResId)
        val indicator = statusIndicator?.background as? GradientDrawable
            ?: GradientDrawable().also {
                it.shape = GradientDrawable.OVAL
                statusIndicator?.background = it
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
        val view = statusIndicator ?: return
        pulseAnimator = ObjectAnimator.ofFloat(view, View.ALPHA, 1.0f, 0.4f).apply {
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
        statusIndicator?.alpha = 1.0f
    }

    private fun appendLog(message: String) {
        val timestamp = dateFormat.format(Date())
        val logLine = "[$timestamp] $message\n"
        activity?.runOnUiThread {
            tvLog?.append(logLine)
            logScrollView?.post {
                logScrollView?.fullScroll(android.view.View.FOCUS_DOWN)
            }
            val text = tvLog?.text?.toString() ?: return@runOnUiThread
            if (text.length > 10000) {
                tvLog?.text = text.substring(text.length - 5000)
            }
        }
    }

    private fun showConfigEditor() {
        val ctx = requireContext()
        // HOME semantics: the daemon resolves settings under filesDir/home
        // (passwd home) — the pre-migration filesDir/.gbot is a dead file.
        val gbotDir = java.io.File(ctx.filesDir, "home/.gbot")
        gbotDir.mkdirs()
        val configFile = java.io.File(gbotDir, "settings.json")
        val currentContent = if (configFile.exists()) configFile.readText()
            else """{"providers":[]}"""

        val edit = android.widget.EditText(ctx).apply {
            setText(currentContent)
            setTypeface(android.graphics.Typeface.MONOSPACE)
            textSize = 12f
            setPadding(32, 24, 32, 24)
            setHorizontallyScrolling(true)
            minLines = 15
        }

        val scroll = android.widget.ScrollView(ctx).apply { addView(edit) }

        androidx.appcompat.app.AlertDialog.Builder(ctx)
            .setTitle("settings.json")
            .setView(scroll)
            .setPositiveButton("Save") { _, _ ->
                try {
                    configFile.writeText(edit.text.toString())
                    appendLog("Config saved to ${configFile.absolutePath}")
                    appendLog("Restarting gbot...")
                    Thread {
                        GbotProcess.stop()
                        GbotProcess.start(ctx) { msg -> appendLog(msg) }
                    }.start()
                } catch (e: Exception) {
                    appendLog("Config save error: ${e.message}")
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    override fun onDestroyView() {
        if (isServerRunning) {
            stopServer()
        }
        stopPulse()
        statusIndicator = null
        tvStatus = null
        tvIpAddress = null
        serverConfigGroup = null
        etServer = null
        etPort = null
        accessibilityIndicator = null
        tvAccessibilityStatus = null
        btnOpenAccessibility = null
        btnToggleServer = null
        tvLog = null
        logScrollView = null
        super.onDestroyView()
    }
}
