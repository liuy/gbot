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

        // Ensure the runit supervisor BEFORE the already-running early return:
        // a daemon can outlive its supervisor (Android's phantom process
        // killer reaps orphaned runsvdir; a stop()/start() cycle kills it
        // too), and skipping the check here left v2ray unsupervised while
        // the daemon looked perfectly healthy.
        val prefixDir = File(context.filesDir, "usr")
        if (prefixDir.isDirectory) ensureTermuxServices(prefixDir, log)

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

        val gbotBin = File(usrBin, "gbot")
        log("gbot: ${gbotBin.absolutePath} exists=${gbotBin.exists()} size=${gbotBin.length()}")

        if (!gbotBin.exists()) {
            log("ERROR: gbot binary not found")
            return false
        }

        ensureTermuxServices(prefixDir, log)

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
                // Go's time.initLocal() is a UTC stub on Android (golang/go#20455);
                // pass the system timezone so gbot can set time.Local from it.
                put("TZ", java.util.TimeZone.getDefault().id)
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

    /** Context-based entry for callers outside the GbotProcess lifecycle. */
    fun ensureTermuxServices(context: Context, log: (String) -> Unit) {
        val prefixDir = File(context.filesDir, "usr")
        if (prefixDir.isDirectory) ensureTermuxServices(prefixDir, log)
    }

    /**
     * Termux-services (runit) supervisor lifecycle. Normally a Termux login
     * shell starts runsvdir via profile.d; this app spawns gbot directly, so
     * nothing revives the supervisor after the app's process group is reaped
     * on restart. Start runsvdir here (idempotent via pgrep) — it then
     * brings up and crash-restarts everything under usr/var/service/ (e.g.
     * v2ray), the systemd-like layer for the embedded Termux environment.
     */
    private fun ensureTermuxServices(prefixDir: File, log: (String) -> Unit) {
        val runsvdir = File(prefixDir, "bin/runsvdir")
        val serviceDir = File(prefixDir, "var/service")
        if (!runsvdir.exists() || !serviceDir.isDirectory) return
        try {
            val check = ProcessBuilder(
                File(prefixDir, "bin/pgrep").absolutePath, "-f", "runsvdir"
            ).redirectErrorStream(true).start()
            val alreadyRunning = check.inputStream.readBytes().isNotEmpty()
            check.waitFor()
            if (alreadyRunning) return
        } catch (_: Exception) {
            // pgrep missing/unusable — fall through and start anyway; a
            // duplicate runsvdir only logs per-service errors, harmless.
        }
        try {
            val logFile = File(prefixDir, "tmp/runsvdir.log")
            ProcessBuilder(runsvdir.absolutePath, serviceDir.absolutePath).apply {
                // runsvdir execs "runsv" via PATH — without Termux's bin on
                // PATH it silently never starts any service.
                environment()["PATH"] = File(prefixDir, "bin").absolutePath + ":/system/bin"
                // The app process cwd is "/" (SELinux-denied); runsvdir stats
                // its cwd at startup and dies with "access denied" there.
                directory(prefixDir)
                // Merge stderr into a log file so per-service output can
                // never fill an unread pipe and block the supervisor.
                redirectErrorStream(true)
                redirectOutput(ProcessBuilder.Redirect.appendTo(logFile))
            }.start()
            log("runsvdir started (termux services supervised)")
        } catch (e: Exception) {
            log("runsvdir start failed: ${e.message}")
        }
    }
}

