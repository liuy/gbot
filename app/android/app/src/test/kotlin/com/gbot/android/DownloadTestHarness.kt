package com.gbot.android

import android.content.ContentProvider
import android.content.ContentValues
import android.content.Context
import android.database.Cursor
import android.database.MatrixCursor
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.MediaStore
import org.robolectric.Shadows.shadowOf
import org.robolectric.shadows.ShadowContentResolver
import java.io.BufferedOutputStream
import java.io.BufferedReader
import java.io.ByteArrayOutputStream
import java.io.FileNotFoundException
import java.io.IOException
import java.io.InputStreamReader
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger

/**
 * Shared scaffolding for the downloader suites. No @Test functions here, so
 * Gradle emits no result XML for this file. The top-level server helpers live
 * in the DownloadTestHarnessKt facade and never load FakeMediaProvider, so
 * the plain-JUnit HTTP test can import them without touching an Android class.
 */

/**
 * Fake ContentProvider behind the "media" authority (MediaStore.AUTHORITY).
 * The production contentResolver.insert/openOutputStream/close/delete path
 * runs unmodified — only the provider behind the authority is fake. insert
 * mints a fresh URI AND registers a fresh stream for it in the shadow's map:
 * a stream pre-registered for one exact URI cannot serve attempt 2 (every
 * retry inserts a new row), and a map miss falls through to the real
 * resolver, which throws "No content provider" instead of the open-failure
 * the multi-attempt tests exercise.
 */
class FakeMediaProvider(
    private val ctx: Context,
    private val registerStreams: Boolean = true, // false -> shadow map miss on openOutputStream
) : ContentProvider() {
    val rows = mutableListOf<Uri>()                           // mint order == attempt order
    val streams = LinkedHashMap<Uri, ByteArrayOutputStream>() // one fresh sink per insert
    val deleted = mutableListOf<Uri>()

    // Test hook, runs on the download worker thread right after the row and
    // its stream are registered — the one deterministic observation point in
    // the middle of a run.
    var onInsert: (() -> Unit)? = null

    // Non-null makes query() answer DISPLAY_NAME with it, standing in for
    // MediaStore's collision rename ("x.zip" stored as "x (1).zip").
    var storedDisplayName: String? = null

    private var seq = 0

    override fun onCreate(): Boolean = true

    override fun insert(uri: Uri, values: ContentValues?): Uri? {
        val u = Uri.withAppendedPath(uri, (++seq).toString()) // content://media/external/downloads/1, /2, …
        rows += u
        val bos = ByteArrayOutputStream()
        streams[u] = bos
        if (registerStreams) shadowOf(ctx.contentResolver).registerOutputStream(u, bos)
        onInsert?.invoke()
        return u
    }

    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?): Int {
        deleted += uri
        return 1
    }

    // Tripwire, not a routed hook. ContentResolver.openOutputStream never
    // reaches any provider member under Robolectric: the shadow answers from
    // its own map, and on a map miss it calls the *real* resolver, which
    // throws "No content provider: <uri>". openFile is a real ContentProvider
    // member (unlike openOutputStream, which is a ContentResolver method and
    // cannot be overridden here at all); this override exists so that if a
    // future Robolectric does route open* through the provider, a missed
    // registerOutputStream surfaces as this unmistakable message instead of
    // masquerading as the production open-failure this suite is testing.
    override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor =
        throw FileNotFoundException("fake media store: no stream registered for $uri")

    override fun query(
        uri: Uri, projection: Array<out String>?, selection: String?,
        selectionArgs: Array<out String>?, sortOrder: String?,
    ): Cursor? {
        val stored = storedDisplayName ?: return null
        val cols = projection ?: arrayOf(android.provider.MediaStore.MediaColumns.DISPLAY_NAME)
        return MatrixCursor(cols).apply { addRow(Array(cols.size) { i -> if (i == 0) stored else null }) }
    }

    override fun getType(uri: Uri): String? = null

    override fun update(
        uri: Uri, values: ContentValues?, selection: String?,
        selectionArgs: Array<out String>?,
    ): Int = 0
}

fun installFakeMediaStore(ctx: Context, registerStreams: Boolean = true): FakeMediaProvider {
    val p = FakeMediaProvider(ctx, registerStreams)
    // registerProviderInternal (public static) feeds the providers map the
    // shadow consults by authority. The map is @Resetter-cleared between test
    // methods, so each test must install its own provider.
    ShadowContentResolver.registerProviderInternal("media", p)
    return p
}

fun insertStatementsFor(ctx: Context, name: String) =
    shadowOf(ctx.contentResolver).insertStatements.filter {
        it.contentValues.getAsString(MediaStore.MediaColumns.DISPLAY_NAME) == name
    }

/**
 * androidx FileProvider parses file_paths.xml once per authority and keeps
 * the strategy (with the roots baked in) in a STATIC map that survives across
 * Robolectric test methods. Under Robolectric those roots are per-test-method
 * temp dirs, so a strategy parsed by an earlier sdk-28 test (Robolectric
 * instantiates the manifest's FileProvider whenever any test builds the
 * application) makes every later API-28 getUriForFile fail with "Failed to
 * find configured root". On-device this cannot happen (one process, one
 * storage root), so the reset is test-only hygiene.
 */
fun resetFileProviderStrategyCache() {
    val field = androidx.core.content.FileProvider::class.java.getDeclaredField("sCache")
    field.isAccessible = true
    (field.get(null) as java.util.HashMap<*, *>).clear()
}

/**
 * Loopback HTTP server standing in for the daemon's artifact endpoint: real
 * sockets, real request bytes — the same framing the device sees. The accept
 * loop owns the listening socket and consumes every connection on a daemon
 * thread, so tests never call accept() themselves: "no connection arrived" is
 * asserted as accepts.get() == 0 after ArtifactDownloader.awaitIdle().
 */
class TestServer private constructor(
    val port: Int,
    val accepts: AtomicInteger,
    val requests: CopyOnWriteArrayList<String>, // request line + headers, per connection
    private val body: ByteArray,
    private val statusLine: String,
    private val truncateTo: Int,
    private val extraHeaders: List<String>,
    private val omitContentLength: Boolean,
    private val silent: Boolean,
) {
    private val socket = ServerSocket(port, 10, InetAddress.getByName("127.0.0.1"))
    private val acceptThread: Thread

    init {
        acceptThread = Thread({ acceptLoop() }, "gbot-test-server-$port")
        acceptThread.isDaemon = true
        acceptThread.start()
    }

    fun url(path: String): String = "http://127.0.0.1:$port/$path"

    fun stop() {
        socket.close()
        acceptThread.join(5000)
    }

    private fun acceptLoop() {
        try {
            while (true) {
                serve(socket.accept())
            }
        } catch (_: IOException) {
            // listening socket closed by stop() — the accept loop's normal exit
        }
    }

    private fun serve(conn: Socket) {
        try {
            conn.use { s ->
                val reader = BufferedReader(InputStreamReader(s.getInputStream(), Charsets.ISO_8859_1))
                val text = StringBuilder()
                while (true) {
                    val line = reader.readLine() ?: break
                    if (line.isEmpty()) break
                    text.append(line).append('\n')
                }
                requests.add(text.toString())
                if (silent) return@use
                val out = BufferedOutputStream(s.getOutputStream())
                out.write((statusLine + "\r\n").toByteArray())
                out.write("Content-Type: application/octet-stream\r\n".toByteArray())
                for (h in extraHeaders) out.write((h + "\r\n").toByteArray())
                // Content-Length must advertise the length the CLIENT validates
                // against: a truncated body advertises the full length (that is
                // what makes the shortfall detectable), an omitted length
                // advertises nothing and the body ends at the close.
                if (!omitContentLength) out.write("Content-Length: ${body.size}\r\n".toByteArray())
                out.write("Connection: close\r\n".toByteArray())
                out.write("\r\n".toByteArray())
                val n = if (truncateTo >= 0) truncateTo else body.size
                out.write(body, 0, n)
                out.flush()
            }
        } catch (_: IOException) {
            // client gave up (timeout/fault paths) — nothing left to serve
        } finally {
            accepts.incrementAndGet()
        }
    }

    companion object {
        fun create(
            body: ByteArray,
            statusLine: String = "HTTP/1.1 200 OK",
            truncateTo: Int = -1,
            extraHeaders: List<String> = emptyList(),
            omitContentLength: Boolean = false,
            silent: Boolean = false,
        ): TestServer {
            val freePort = java.net.ServerSocket(0).use { it.localPort }
            return TestServer(
                freePort,
                AtomicInteger(0), CopyOnWriteArrayList(),
                body, statusLine, truncateTo, extraHeaders, omitContentLength, silent,
            )
        }
    }
}

fun startServer(
    body: ByteArray,
    statusLine: String = "HTTP/1.1 200 OK",
    truncateTo: Int = -1,
    extraHeaders: List<String> = emptyList(),
    omitContentLength: Boolean = false,
): TestServer = TestServer.create(body, statusLine, truncateTo, extraHeaders, omitContentLength)

fun startAcceptAndCloseServer(): TestServer =
    TestServer.create(ByteArray(0), silent = true)
