package com.gbot.android

import android.content.Context
import android.util.Log
import java.io.File
import java.io.FileOutputStream

/**
 * Starts and manages the gbot daemon as a child process.
 * gbot runs from filesDir/usr/bin/gbot — targetSdk 28 allows exec from
 * filesDir, so no jniLibs or LD_PRELOAD needed.
 */
object GbotProcess {

    private const val TAG = "GbotProcess"
    private const val LOG_FILE = "gbot-stdout.log"

    private val lock = Any()
    @Volatile private var process: Process? = null
    val logBuffer = StringBuffer()

    fun start(context: Context, onLog: (String) -> Unit): Boolean = synchronized(lock) {
        val log: (String) -> Unit = { msg ->
            logBuffer.append("$msg\n")
            onLog(msg)
        }

        if (process?.isAlive == true) {
            log("gbot already running")
            return true
        }

        log("Extracting bootstrap...")
        val usrBin = BootstrapInstaller.ensureInstalled(
            context,
            onLog = { msg -> log(msg) },
            onError = { err -> log("Bootstrap error: $err") }
        ) ?: run {
            log("ERROR: Bootstrap installation failed")
            return false
        }

        val prefixDir = File(context.filesDir, "usr")
        val gbotBin = File(usrBin, "gbot")
        log("gbot: ${gbotBin.absolutePath} exists=${gbotBin.exists()} size=${gbotBin.length()}")

        if (!gbotBin.exists()) {
            log("ERROR: gbot binary not found")
            return false
        }

        // Always overwrite gbot/rg with the APK assets — BootstrapInstaller
        // skips injection when the Termux prefix is already installed (version
        // file match), so without this an app update would keep the old binary.
        // gbot is never running here (alive check above returns early).
        overwriteFromAsset(context, gbotBin, "gbot-arm64")
        val rgBin = File(usrBin, "rg")
        overwriteFromAsset(context, rgBin, "rg-arm64")

        val homeDir = context.filesDir.absolutePath
        val logFile = File(context.filesDir, LOG_FILE)

        log("Starting: ${gbotBin.absolutePath} --daemon")
        log("HOME=$homeDir")

        try {
            val pb = ProcessBuilder(gbotBin.absolutePath, "--daemon")
                .directory(context.filesDir)
                .redirectOutput(logFile)
                .redirectErrorStream(true)

            pb.environment().apply {
                put("HOME", homeDir)
                put("GBOT_BASH_PATH", "$usrBin/bash")
                put("PATH", "$usrBin:/system/bin:/system/xbin")
                put("LD_LIBRARY_PATH", "$prefixDir/lib")
                put("TMPDIR", "$prefixDir/tmp")
                put("PREFIX", "$prefixDir")
                put("GODEBUG", "netdns=cgo")
            }

            process = pb.start()
            log("Process started")
            return true

            log("gbot running successfully")
            return true
        } catch (e: Exception) {
            log("EXCEPTION: ${e.javaClass.name}: ${e.message}")
            Log.e(TAG, "Failed to start gbot", e)
            return false
        }
    }

    fun stop() {
        synchronized(lock) {
            process?.let {
                Log.i(TAG, "Stopping gbot")
                it.destroy()
            }
            process = null
        }
    }

    /**
     * Overwrites [dest] with the APK asset [assetName] unconditionally.
     * BootstrapInstaller skips injection when the Termux prefix is already
     * installed (version file match), so an app update would keep the old
     * binary without this. Called only when gbot is not running.
     */
    private fun overwriteFromAsset(
        context: Context,
        dest: File,
        assetName: String,
    ) {
        context.assets.open(assetName).use { asset ->
            FileOutputStream(dest).use { asset.copyTo(it) }
        }
        android.system.Os.chmod(dest.absolutePath, 0x1C0) // 0700
    }
}
