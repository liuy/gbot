package com.gbot.android

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import androidx.annotation.RequiresApi
import androidx.core.app.NotificationCompat
import androidx.core.content.FileProvider
import com.gbot.android.server.GbotWebSocketClient
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.io.OutputStream
import java.net.HttpURLConnection
import java.net.MalformedURLException
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference

/**
 * In-process replacement for the system DownloadManager: the WebView's
 * download URLs are streamed here over the app's own network stack, because
 * the system downloader's JobScheduler queue stalls on the target device
 * (tasks sit "queued" forever, no notification) and a separate provider
 * process is not guaranteed the per-app VPN routing the daemon is reachable
 * through. One download at a time on a daemon worker, 3 attempts, failures
 * surface as a one-shot notification plus an event-log line.
 *
 * Progress is NOT rendered here: the downloader only publishes lifecycle
 * state ([state]) and the foreground service folds it into its existing
 * notification. The one-shot completion/failure notifications are the
 * downloader's own ids and channel.
 */
object ArtifactDownloader {

    const val CHANNEL_ID = "gbot_download_channel"
    private const val NOTIFICATION_ID_BASE = 2001 // service's persistent notification is 1001
    internal const val CONNECT_TIMEOUT_MS = 15_000
    internal const val READ_TIMEOUT_MS = 30_000
    internal const val MAX_ATTEMPTS = 3
    internal const val RETRY_DELAY_MS = 2_000L

    // ≥500 ms time floor between progress re-renders (never a byte-delta
    // rule): with Content-Length absent (chunked/Connection: close) there is
    // no total a "1%" could be a fraction of, and on huge bodies 1 % is a
    // multi-MB gap — a time floor bounds the notify rate for both.
    internal const val PROGRESS_NOTIFY_MS = 500L

    /** Non-retryable: the HTTP layer answered definitively (non-2xx, unsupported scheme). */
    internal class HttpFailure(reason: String) : Exception(reason)

    /** Non-retryable: the destination could not be opened (insert, stream, mkdirs). */
    internal class SinkFailure(reason: String) : Exception(reason)

    /**
     * Download lifecycle the service notification renders. Downloading is
     * replaced (never mutated) so readers of [state] always see a coherent
     * snapshot; Finished (not Idle) ends every run because the state's only
     * consumer renders anything-but-Downloading as the plain connection form,
     * and "a download just ended" stays observable without a second channel.
     */
    sealed class DownloadState {
        object Idle : DownloadState()
        data class Downloading(
            val name: String,
            val bytesDone: Long,
            val bytesTotal: Long, // -1 = unknown (chunked / Connection: close)
            val startedAt: Long,
        ) : DownloadState()
        object Finished : DownloadState()
    }

    /** Current lifecycle state — written by the worker, read by the service renderer. */
    val state = AtomicReference<DownloadState>(DownloadState.Idle)

    // The service owns rendering of its persistent notification exclusively:
    // it registers this hook at onCreate and the downloader only pokes it
    // (throttled) when [state] changed. Null when no service is running —
    // progress then simply renders nowhere.
    @Volatile internal var requestRender: (() -> Unit)? = null

    internal data class Sink(
        val output: OutputStream,
        val uri: Uri,
        val destination: String, // human-readable, for the event log
        val file: File?,         // non-null only on API 28 (what discardSink deletes)
    )
    internal data class Saved(val bytes: Long, val uri: Uri, val mime: String, val destination: String)

    private val nextId = AtomicInteger(NOTIFICATION_ID_BASE)

    // Static state must stay java.*-only: the plain-JUnit tests load this object
    // and any Android type in <clinit> would hit the stubbed android.jar.
    private val executor = Executors.newSingleThreadExecutor { r ->
        // Daemon: a non-daemon idle worker keeps the Gradle test JVM alive
        // after the suite finishes.
        Thread(r, "gbot-download").apply { isDaemon = true }
    }
    @Volatile private var lastRenderAt = 0L

    fun start(ctx: Context, url: String, name: String, mimeType: String?) {
        val app = ctx.applicationContext
        executor.execute {
            try {
                run(app, url, name, mimeType)
            } catch (t: Throwable) {
                // An exception escaping the worker would end the download with
                // no notification and no log line at all.
                GbotProcess.appendEvent("download", "unexpected: ${t.message}")
            }
        }
    }

    internal fun run(
        ctx: Context,
        url: String,
        name: String,
        mimeType: String?,
        retryDelayMs: Long = RETRY_DELAY_MS,
    ): String? {
        ensureChannel(ctx)
        GbotProcess.appendEvent("download", "start $name from $url")
        // Fresh 0 resets the progress throttle so the first chunk always
        // renders — without it a download started <500 ms after the previous
        // one finished would show no progress until its second window.
        lastRenderAt = 0L
        state.set(DownloadState.Downloading(name, 0, -1, System.currentTimeMillis()))
        var reason = "unknown error"
        var saved: Saved? = null
        for (attempt in 1..MAX_ATTEMPTS) {
            try {
                saved = attemptOnce(ctx, url, name, mimeType)
                break
            } catch (e: Exception) {
                // HttpFailure/SinkFailure extend Exception, not IOException,
                // precisely so this classification is exact.
                val retryable = e is IOException
                reason = e.message ?: e.javaClass.simpleName
                if (!retryable) break
                if (attempt < MAX_ATTEMPTS) {
                    GbotProcess.appendEvent("download", "$name attempt $attempt failed: $reason; retrying")
                    Thread.sleep(retryDelayMs)
                }
            }
        }
        // Terminal state + unconditional re-render must precede the one-shot
        // notification: id 1001 leaves "downloading" form even if the
        // completion post below throws. No notify* call sits inside the retry
        // try — one thrown in there would be classified non-retryable, break
        // the loop with saved == null, and post a second notification for a
        // download that already completed.
        state.set(DownloadState.Finished)
        requestRender?.invoke()
        val done = saved
        if (done != null) {
            GbotProcess.appendEvent("download", "$name done (${done.bytes} bytes) -> ${done.destination}")
            runCatching { notifyDone(ctx, name, done) }
                .onFailure { GbotProcess.appendEvent("download", "$name notify failed: ${it.message}") }
            return null
        }
        GbotProcess.appendEvent("download", "$name failed: $reason")
        runCatching { notifyFailure(ctx, name, reason) }
            .onFailure { GbotProcess.appendEvent("download", "$name notify failed: ${it.message}") }
        return reason
    }

    private fun attemptOnce(ctx: Context, url: String, name: String, mimeType: String?): Saved {
        // openConnection reads conn.responseCode before openSink runs, so an
        // HTTP status error or connection-level fault never opens a
        // destination row — no zero-byte row is possible for those faults.
        val conn = openConnection(url)
        var sink: Sink? = null
        try {
            val total = conn.contentLengthLong
            val startedAt = (state.get() as? DownloadState.Downloading)?.startedAt
                ?: System.currentTimeMillis()
            sink = openSink(ctx, name, mimeType)
            val bytes = pump(conn, sink.output) { done ->
                state.set(DownloadState.Downloading(name, done, total, startedAt))
                val now = System.currentTimeMillis()
                if (now - lastRenderAt >= PROGRESS_NOTIFY_MS) {
                    lastRenderAt = now
                    requestRender?.invoke()
                }
            }
            // MediaStore commits the row + size here — closing is the commit.
            sink.output.close()
            return Saved(bytes, sink.uri, resolvedMime(name, mimeType), sink.destination)
        } catch (e: Exception) {
            sink?.let {
                runCatching { it.output.close() }
                discardSink(ctx, it)
            }
            throw e
        } finally {
            conn.disconnect()
        }
    }

    internal fun openConnection(url: String): HttpURLConnection {
        val conn = try {
            java.net.URL(url).openConnection() as HttpURLConnection
        } catch (e: MalformedURLException) {
            throw HttpFailure("unsupported url: $url")
        } catch (e: ClassCastException) {
            throw HttpFailure("unsupported url scheme: $url")
        }
        // Explicit identity: OkHttp (Android's HttpURLConnection) adds "gzip" itself
        // and, when it does, hides Content-Length — pump's byte-count guard would
        // silently stop checking. The daemon gzips only what its 512-byte sniff calls
        // compressible, so pinning the encoding is what makes the length trustworthy.
        conn.setRequestProperty("Accept-Encoding", "identity")
        conn.connectTimeout = CONNECT_TIMEOUT_MS
        conn.readTimeout = READ_TIMEOUT_MS
        // Deliberate guard against a future setCache(...): the HTTP cache must
        // not buffer a 394 MB artifact into the app's cache dir. (The JDK
        // already defaults this to false, so no test can pin it.)
        conn.useCaches = false
        try {
            val code = conn.responseCode
            if (code !in 200..299) {
                conn.disconnect()
                throw HttpFailure("HTTP $code")
            }
        } catch (e: IOException) {
            conn.disconnect()
            throw e
        }
        return conn
    }

    internal fun pump(
        conn: HttpURLConnection,
        out: OutputStream,
        onProgress: ((Long) -> Unit)? = null,
    ): Long {
        // -1 = chunked/unknown length → skip validation. == 0 is a legitimate
        // success (empty artifact), so the guard is >= 0, never > 0.
        val expected = conn.contentLengthLong
        val buf = ByteArray(8192)
        var written = 0L
        conn.inputStream.use { input ->
            while (true) {
                val n = input.read(buf)
                if (n < 0) break
                out.write(buf, 0, n)
                written += n
                onProgress?.invoke(written)
            }
        }
        if (expected >= 0 && written != expected) {
            throw IOException("wrote $written of $expected bytes")
        }
        return written
    }

    internal fun awaitIdle(timeoutMs: Long = 10_000): Boolean {
        val drained = java.util.concurrent.CountDownLatch(1)
        executor.execute { drained.countDown() }
        return drained.await(timeoutMs, TimeUnit.MILLISECONDS)
    }

    private fun resolvedMime(name: String, mimeType: String?): String =
        mimeType?.ifBlank { null } ?: GbotWebSocketClient.mimeTypeFor(name)

    private fun openSink(ctx: Context, name: String, mimeType: String?): Sink {
        val mime = resolvedMime(name, mimeType)
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            mediaSink(ctx, name, mime)
        } else {
            fileSink(ctx, name)
        }
    }

    // API 28: no MediaStore.Downloads exists, and WRITE_EXTERNAL_STORAGE was
    // deliberately removed from this app (asking for it was pure interruption).
    // The app-scoped external dir costs zero permissions; the event log names
    // the exact destination so the file stays findable.
    private fun fileSink(ctx: Context, name: String): Sink {
        val base = ctx.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)
            ?: throw SinkFailure("open output: no external files dir")
        val dir = File(base, "gbot")
        // Guarded, not a bare mkdirs(): a silent false here would fall through
        // to a FileOutputStream FileNotFoundException, be classified as plain
        // IOException, and burn all three attempts on an unfixable condition.
        if (!dir.exists() && !dir.mkdirs()) {
            throw SinkFailure("open output: cannot create ${dir.absolutePath}")
        }
        val file = File(dir, name)
        val uri = FileProvider.getUriForFile(ctx, "${ctx.packageName}.fileprovider", file)
        return Sink(FileOutputStream(file), uri, file.absolutePath, file)
    }

    @RequiresApi(Build.VERSION_CODES.Q)
    private fun mediaSink(ctx: Context, name: String, mime: String): Sink {
        // No SIZE: it is unknown before the response headers arrive, and
        // MediaStore fills it from the stream on close.
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, name)
            put(MediaStore.MediaColumns.MIME_TYPE, mime)
            put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/gbot")
        }
        val uri = ctx.contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
            ?: throw SinkFailure("open output: MediaStore insert failed")
        val out = try {
            ctx.contentResolver.openOutputStream(uri, "w")
        } catch (e: Exception) {
            ctx.contentResolver.delete(uri, null, null)
            throw SinkFailure("open output: ${e.message ?: e.javaClass.simpleName}")
        } ?: run {
            ctx.contentResolver.delete(uri, null, null)
            throw SinkFailure("open output: openOutputStream returned null")
        }
        // MediaStore renames on collision ("x.zip" → "x (1).zip") without
        // touching the inserted values, so the real display name comes from
        // the stored row, not from our request.
        var displayName = name
        ctx.contentResolver.query(
            uri, arrayOf(MediaStore.MediaColumns.DISPLAY_NAME), null, null, null,
        )?.use { c ->
            if (c.moveToFirst()) c.getString(0)?.let { displayName = it }
        }
        return Sink(out, uri, "Download/gbot/$displayName", null)
    }

    private fun discardSink(ctx: Context, sink: Sink) {
        if (sink.file != null) {
            runCatching { sink.file.delete() }
        } else {
            runCatching { ctx.contentResolver.delete(sink.uri, null, null) }
        }
    }

    private fun ensureChannel(ctx: Context) {
        val channel = NotificationChannel(
            CHANNEL_ID,
            ctx.getString(R.string.download_channel_name),
            NotificationManager.IMPORTANCE_DEFAULT,
        ).apply {
            description = ctx.getString(R.string.download_channel_description)
        }
        // DEFAULT, not the service channel's LOW: with progress riding the
        // silent service notification, the completion notification is the
        // only audible signal a download finished.
        ctx.getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
    }

    private fun notifyDone(ctx: Context, name: String, saved: Saved) {
        // APK taps go through InstallTapActivity: it branches between the
        // installer and the unknown-sources switch at TAP time, which a
        // PendingIntent baked at notify time cannot do (and Android 12
        // forbids trampolining the tap through a receiver). Non-APK files
        // need no install permission — view them directly.
        val pi = if (name.endsWith(".apk", ignoreCase = true)) {
            val tap = Intent(ctx, InstallTapActivity::class.java).apply {
                setDataAndType(saved.uri, saved.mime)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            PendingIntent.getActivity(ctx, nextId.incrementAndGet(), tap, PendingIntent.FLAG_IMMUTABLE)
        } else {
            val view = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(saved.uri, saved.mime)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            PendingIntent.getActivity(ctx, nextId.incrementAndGet(), view, PendingIntent.FLAG_IMMUTABLE)
        }
        post(ctx, nextId.incrementAndGet(), ctx.getString(R.string.download_done_title),
            ctx.getString(R.string.download_done_saved, saved.destination), pi)
    }

    private fun notifyFailure(ctx: Context, name: String, reason: String) {
        // No content intent: there is nothing to open.
        post(
            ctx, nextId.incrementAndGet(),
            ctx.getString(R.string.download_failed_title), "$name: $reason", null,
        )
    }

    private fun post(ctx: Context, id: Int, title: String, text: String, intent: PendingIntent?) {
        val builder = NotificationCompat.Builder(ctx, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_menu_share)
            .setContentTitle(title)
            .setContentText(text)
            .setAutoCancel(true)
        if (intent != null) builder.setContentIntent(intent)
        ctx.getSystemService(NotificationManager::class.java).notify(id, builder.build())
    }
}
