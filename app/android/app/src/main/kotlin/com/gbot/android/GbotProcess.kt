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

    /** Appends one app-side event line ("[HH:mm:ss] message") to logBuffer —
     *  the WUI app-log panel's feed. Truncation matches the old Control tab
     *  log view: over 10000 chars, keep the last 5000. All appends share the
     *  logBuffer monitor: the WS client, the daemon stdout pump and the
     *  bridge reader run on different threads. */
    fun appendEvent(message: String) {
        synchronized(logBuffer) {
            val ts = java.text.SimpleDateFormat("HH:mm:ss", java.util.Locale.getDefault())
                .format(java.util.Date())
            logBuffer.append("[$ts] $message\n")
            val len = logBuffer.length
            if (len > 10000) logBuffer.delete(0, len - 5000)
        }
    }

    /** Logcat dual-write for scattered Log.x sites: logcat keeps the original
     *  line, the panel gets the same text prefixed with the tag so the source
     *  is identifiable without logcat. */
    fun appendEvent(tag: String, message: String) = appendEvent("$tag: $message")

    /** Why the previous app instance died (ApplicationExitInfo, Android 11+).
     *  Set by MainActivity at cold start, passed to the daemon as
     *  GBOT_PREV_EXIT so the death reason lands in gbot.log. */
    @Volatile var lastExitInfo: String? = null

    /** Best-effort read of the most recent process-exit record. Answers
     *  "why did the last instance die" — OOM/LMK vs ANR vs signal vs user.
     *  Returns a diagnostic string instead of null on no-records/query
     *  failure (an invisible null here once silenced the whole feature);
     *  null only on <API 30. */
    fun readLastExitReason(context: Context): String? {
        if (android.os.Build.VERSION.SDK_INT < 30) return null
        return try {
            val am = context.getSystemService(Context.ACTIVITY_SERVICE) as android.app.ActivityManager
            // pid=0 → all processes of this package; maxNum=1 → only the
            // most recent record (the list is sorted most-recent-first).
            val records = am.getHistoricalProcessExitReasons(context.packageName, 0, 1)
            val e = records.firstOrNull()
                ?: return "no exit records (queried, system kept ${records.size})"
            val reason = when (e.reason) {
                android.app.ApplicationExitInfo.REASON_LOW_MEMORY -> "LOW_MEMORY"
                android.app.ApplicationExitInfo.REASON_ANR -> "ANR"
                android.app.ApplicationExitInfo.REASON_CRASH -> "CRASH"
                android.app.ApplicationExitInfo.REASON_CRASH_NATIVE -> "CRASH_NATIVE"
                android.app.ApplicationExitInfo.REASON_SIGNALED -> "SIGNALED"
                android.app.ApplicationExitInfo.REASON_USER_REQUESTED -> "USER_REQUESTED"
                android.app.ApplicationExitInfo.REASON_USER_STOPPED -> "USER_STOPPED"
                android.app.ApplicationExitInfo.REASON_EXCESSIVE_RESOURCE_USAGE -> "EXCESSIVE_RESOURCE"
                android.app.ApplicationExitInfo.REASON_EXIT_SELF -> "EXIT_SELF"
                android.app.ApplicationExitInfo.REASON_OTHER -> "OTHER"
                else -> "REASON_${e.reason}"
            }
            val ts = java.text.SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ssXXX", java.util.Locale.US)
                .format(java.util.Date(e.timestamp))
            buildString {
                append("reason=").append(reason)
                append(" status=").append(e.status)
                append(" proc=").append(e.processName)
                append(" at=").append(ts)
                e.description?.takeIf { it.isNotBlank() }?.let { append(" desc=").append(it) }
            }
        } catch (e: Exception) {
            // Surface the failure through the same pipe — an invisible
            // catch here cost us a whole debugging session.
            Log.w(TAG, "exit-info unavailable: ${e.message}")
            return "exit-info query failed: ${e.javaClass.simpleName}: ${e.message}"
        }
    }

    fun start(context: Context, onLog: (String) -> Unit): Boolean = synchronized(lock) {
        val log: (String) -> Unit = { msg ->
            synchronized(logBuffer) {
                logBuffer.append("$msg\n")
            }
            onLog(msg)
        }

        // Ensure the runit supervisor BEFORE the already-running early return:
        // a daemon can outlive its supervisor (Android's phantom process
        // killer reaps orphaned runsvdir; a stop()/start() cycle kills it
        // too), and skipping the check here left v2ray unsupervised while
        // the daemon looked perfectly healthy.
        val prefixDir = File(context.filesDir, "usr")
        if (prefixDir.isDirectory) ensureTermuxServices(prefixDir, log)

        lastExitInfo?.let { log("last exit: $it") }
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

        // Termux-standard layout: PREFIX=filesDir/usr, HOME=filesDir/home.
        // One home only — the passwd-DB home for this uid is filesDir/home,
        // so Java user.home, ssh, and $HOME all resolve to the SAME place.
        val homeDir = File(context.filesDir, "home").apply { mkdirs() }.absolutePath

        log("Starting: ${gbotBin.absolutePath} --daemon")
        log("HOME=$homeDir")

        try {
            val pb = ProcessBuilder(gbotBin.absolutePath, "--daemon")
                .directory(File(context.filesDir, "home"))
                .redirectErrorStream(true)

            pb.environment().apply {
                put("HOME", homeDir)
                put("GBOT_BASH_PATH", "$usrBin/bash")
                put("PATH", "$usrBin:/system/bin:/system/xbin")
                put("LD_LIBRARY_PATH", "$prefixDir/lib")
                put("TMPDIR", "$prefixDir/tmp")
                put("PREFIX", "$prefixDir")
                put("GODEBUG", "netdns=cgo")
                // App-supervised mode: REUSEPORT binds (no tableflip — its
                // handed-over child is an orphan the phantom killer reaps),
                // TERM closes client sockets with 1012, and /api/admin/
                // stepdown can hand the PID lock to a replacement.
                put("GBOT_SUPERVISED", "1")
                // Go's time.initLocal() is a UTC stub on Android (golang/go#20455);
                // pass the system timezone so gbot can set time.Local from it.
                put("TZ", java.util.TimeZone.getDefault().id)
                // Death reason of the previous instance → gbot.log header.
                lastExitInfo?.let {
                    put("GBOT_PREV_EXIT", it)
                    lastExitInfo = null  // consumed by THIS daemon spawn
                }
            }

            val proc = pb.start()
            process = proc
            // Pipe gbot stdout/stderr into logBuffer (surfaced in the WUI
            // app-log panel) so crash messages and panic traces are visible.
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

    /** App-supervised zero-downtime restart (the WUI restart button's
     *  Android path — see GBOT_SUPERVISED in the daemon). Orchestration:
     *  1. POST /api/admin/stepdown — the daemon releases its PID lock but
     *     KEEPS SERVING (busy → 409, we abort, nothing changed);
     *  2. spawn the replacement, which binds the same port via SO_REUSEPORT
     *     (an app-owned direct child, never an orphan);
     *  3. poll GET /api/admin/restart until it answers with a pid different
     *     from the predecessor's — REUSEPORT routes accepts to either
     *     process during the overlap, so a changed pid proves the
     *     replacement is serving;
     *  4. TERM the predecessor: its TERM handler closes client sockets
     *     with 1012 (WUI upgrade fast-reconnect) and it exits onto the
     *     replacement.
     *  Failure at any point after stepdown leaves the predecessor serving
     *  untouched (its lock self-reclaims after a TTL) — worst case equals
     *  the status quo, never worse. */
    fun restartSupervised(context: Context, onLog: (String) -> Unit): Boolean = synchronized(lock) {
        val log: (String) -> Unit = { msg ->
            synchronized(logBuffer) { logBuffer.append("$msg\n") }
            onLog(msg)
        }
        val predecessor = process
        if (predecessor?.isAlive != true) {
            log("restart: no live daemon — plain start")
            return start(context, onLog)
        }
        // Fetch the predecessor's identity BEFORE touching any lock state:
        // without it the probe can never confirm a replacement, so spawning
        // one would be guaranteed churn — abort while nothing changed.
        val predecessorPid = httpAdmin("GET", "/api/admin/restart")
            .second?.let { parseAdminPid(it) }
        if (predecessorPid == null) {
            log("restart: daemon not answering admin endpoint — keeping daemon")
            return false
        }
        when (httpAdmin("POST", "/api/admin/stepdown").first) {
            200 -> {}
            409 -> { log("restart: daemon busy — refused"); return false }
            else -> { log("restart: stepdown failed — keeping daemon"); return false }
        }
        // start() early-returns "already running" while the predecessor is
        // tracked — detach it first so start() spawns the replacement.
        process = null
        if (!start(context, onLog)) {
            process = predecessor
            log("restart: replacement spawn failed — predecessor still serving")
            return false
        }
        val replacement = process!!
        val ready = pollForReplacementPid(
            fetchPid = { httpAdmin("GET", "/api/admin/restart").second?.let { parseAdminPid(it) } },
            oldPid = predecessorPid,
        )
        if (ready) {
            appendEvent(TAG, "Stopping gbot (predecessor)")
            Log.i(TAG, "Stopping gbot (predecessor)")
            predecessor.destroy()
            log("restart: replacement serving — predecessor draining")
            return true
        }
        if (predecessor.isAlive) {
            log("restart: replacement not ready — discarding it, predecessor continues")
            replacement.destroy()
            process = predecessor
            return false
        }
        // Predecessor died during the probe — the replacement is all we
        // have; keep it and let its own boot be the verdict.
        log("restart: predecessor lost during handover — keeping replacement")
        true
    }

    /** One admin-endpoint round trip: (httpStatus, body). Status -1 =
     *  connect/read failure. Loopback only, 3 s budgets — the daemon is
     *  local or something is very wrong. "Connection: close" defeats the
     *  JVM's keep-alive pool: a pooled TCP connection is pinned to ONE
     *  REUSEPORT listener, so a restart-overlap probe reusing it would
     *  read the predecessor's pid forever (2026-09-25: 20 s probes that
     *  never saw the serving replacement). Fresh connections hash to
     *  either listener — the probe flips within a few polls. */
    internal fun httpAdmin(method: String, path: String, port: Int = 8765): Pair<Int, String?> {
        val conn = java.net.URL("http://127.0.0.1:$port$path").openConnection()
            as java.net.HttpURLConnection
        conn.requestMethod = method
        conn.setRequestProperty("Connection", "close")
        conn.connectTimeout = 3000
        conn.readTimeout = 3000
        return try {
            val code = conn.responseCode
            val body = if (code in 200..399) {
                conn.inputStream.bufferedReader().use { it.readText() }
            } else null
            code to body
        } catch (e: Exception) {
            -1 to null
        } finally {
            conn.disconnect()
        }
    }

    /** Pulls "pid" out of the GET /api/admin/restart payload. Hand-rolled
     *  (no org.json): local JVM unit tests stub org.json into throwers, and
     *  this parse is exactly what the restart handover depends on. */
    internal fun parseAdminPid(json: String): Long? =
        Regex("\"pid\"\\s*:\\s*(\\d+)").find(json)?.groupValues?.get(1)?.toLongOrNull()

    /** True once fetchPid yields any pid other than the predecessor's.
     *  Clock and sleep injectable for tests. */
    internal fun pollForReplacementPid(
        fetchPid: () -> Long?,
        oldPid: Long,
        deadlineMs: Long = 20_000,
        sleepMs: Long = 250,
        now: () -> Long = System::currentTimeMillis,
        sleep: (Long) -> Unit = Thread::sleep,
    ): Boolean {
        val deadline = now() + deadlineMs
        while (now() < deadline) {
            val pid = fetchPid()
            if (pid != null && pid != oldPid) return true
            sleep(sleepMs)
        }
        return false
    }

    fun stop() {
        synchronized(lock) {
            process?.let {
                Log.i(TAG, "Stopping gbot")
                appendEvent(TAG, "Stopping gbot")
                it.destroy()
            }
            process = null
        }
    }

    /** Context-based entry — called from MainActivity.onResume (every
 * foreground return) and from start() (before its early return). */
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

