package com.gbot.android

import android.content.Context
import android.util.Log
import java.io.File

/**
 * Starts and manages the gbot daemon as a child process.
 * gbot runs from filesDir/usr/bin/gbot — targetSdk 28 allows exec from
 * filesDir, so no jniLibs or LD_PRELOAD needed.
 */
object GbotProcess {

    private const val TAG = "GbotProcess"

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

        // gbot/rg injection is handled by BootstrapInstaller.ensureInstalled()
        // based on BOOTSTRAP_VERSION. No unconditional overwrite here — this
        // allows on-device builds (make build-android + cp) to survive restarts.

        val homeDir = context.filesDir.absolutePath

        log("Starting: ${gbotBin.absolutePath} --daemon")
        log("HOME=$homeDir")

        try {
            val pb = ProcessBuilder(gbotBin.absolutePath, "--daemon")
                .directory(context.filesDir)
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
            // Pipe gbot stdout/stderr to Control tab logBuffer so crash
            // messages and panic traces are visible there.
            val proc = process!!
            Thread {
                try {
                    proc.inputStream.bufferedReader().useLines { lines ->
                        for (line in lines) {
                            synchronized(logBuffer) {
                                logBuffer.append("gbot: $line\n")
                            }
                        }
                    }
                } catch (_: Exception) {}
            }.start()
            log("Process started")
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
}

