package com.gbot.android

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.provider.Settings

/**
 * Transparent branch point for download-notification taps on .apk files:
 * with the unknown-sources switch on, hand the APK to the installer;
 * without it, deep-link the user to the per-app switch. The notification's
 * PendingIntent cannot branch at tap time, and Android 12 forbids routing
 * the tap through a receiver — hence this activity.
 */
class InstallTapActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val uri = intent.data
        val mime = intent.type
        if (uri == null || mime == null) {
            finish()
            return
        }
        if (packageManager.canRequestPackageInstalls()) {
            startActivity(Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, mime)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            })
        } else {
            startActivity(
                Intent(
                    Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                    Uri.parse("package:$packageName"),
                ),
            )
        }
        finish()
    }
}
