package com.gbot.android

import android.webkit.JavascriptInterface

/**
 * Read-only log bridge exposed to the WUI as window.GBotAppLogs: the settings
 * page's app-log panel reads GbotProcess.logBuffer through it. Only tail/clear
 * — the page must not be able to inject lines. Methods run on WebView's
 * JavaBridge background thread, hence the logBuffer synchronization.
 */
class AppLogsBridge {

    companion object {
        // Mirrored by the WUI as its APP_LOGS_BRIDGE constant — rename both
        // together or the panel silently degrades to the unavailable hint.
        const val BRIDGE_NAME = "GBotAppLogs"
    }

    /** Last n lines, newline-joined; "" when empty or n <= 0. */
    @JavascriptInterface
    fun tail(n: Int): String = synchronized(GbotProcess.logBuffer) {
        val content = GbotProcess.logBuffer.toString().trim('\n')
        if (n <= 0 || content.isEmpty()) return ""
        content.split('\n').takeLast(n).joinToString("\n")
    }

    @JavascriptInterface
    fun clear() {
        synchronized(GbotProcess.logBuffer) { GbotProcess.logBuffer.setLength(0) }
    }
}
