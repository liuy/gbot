package com.gbot.android.server

import android.content.Intent
import com.google.common.truth.Truth.assertThat
import com.gbot.android.service.MobileAccessibilityService
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.io.ByteArrayOutputStream
import java.nio.ByteBuffer

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class WebSocketCommandServerTest {

	private val logs = mutableListOf<String>()
	private val connCounts = mutableListOf<Int>()
	private val server = WebSocketCommandServer(
		appContext = RuntimeEnvironment.getApplication(),
		port = 0,
		onLog = { logs += it },
		onConnectionChange = { connCounts += it }
	)
	private lateinit var service: MobileAccessibilityService

	@Before
	fun setup() {
		// Bind to an OS-assigned free port so shutdown() exercises the started path (no NPE).
		server.start()
		Thread.sleep(200)
	}

	@After
	fun teardown() {
		server.shutdown()
		// A built service, if any test created one, leaves the static instance/isRunning set; clear it.
		if (this::service.isInitialized) service.onDestroy()
	}

	@Test
	fun onOpen_addsClient_emitsCount1() {
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		assertThat(connCounts.last()).isEqualTo(1)
		assertThat(logs.any { it.startsWith("Client connected:") }).isTrue()
	}

	@Test
	fun onOpen_withBadToken_rejectsAndCloses() {
		server.authToken = "secret"
		val conn = FakeWebSocket()

		server.onOpen(conn, FakeHandshake(authHeader = "wrong"))

		assertThat(connCounts).doesNotContain(1)
		assertThat(logs.any { it.contains("Client rejected (bad token)") }).isTrue()
		assertThat(conn.closed).isTrue()
	}

	@Test
	fun onOpen_withCorrectToken_accepts() {
		server.authToken = "secret"
		val conn = FakeWebSocket()

		server.onOpen(conn, FakeHandshake(authHeader = "Bearer secret"))

		assertThat(connCounts.last()).isEqualTo(1)
	}

	@Test
	fun onOpen_nullToken_accepts() {
		assertThat(server.authToken).isNull()
		val conn = FakeWebSocket()

		server.onOpen(conn, FakeHandshake())

		assertThat(connCounts.last()).isEqualTo(1)
	}

	@Test
	fun onClose_removesClient_emitsCount() {
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())
		connCounts.clear()
		logs.clear()

		server.onClose(conn, 1000, "bye", false)

		assertThat(connCounts.last()).isEqualTo(0)
		assertThat(logs.any { it.startsWith("Client disconnected:") }).isTrue()
	}

	@Test
	fun getConnectionCount_reflectsState() {
		server.onOpen(FakeWebSocket(), FakeHandshake())
		server.onOpen(FakeWebSocket(), FakeHandshake())

		assertThat(server.getConnectionCount()).isEqualTo(2)
	}

	@Test
	fun onMessage_missingCommandField_sendsError() {
		// Default: no accessibility service instance is set.
		assertThat(MobileAccessibilityService.instance).isNull()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		server.onMessage(conn, """{"id":"1"}""")
		awaitSent(conn)

		// Gson HTML-escapes the single quotes in the error string as \u0027.
		val sent = conn.sent.last()
		assertThat(sent).contains(""""success":false""")
		assertThat(sent).contains("Missing \\u0027command\\u0027 field")
	}

	@Test
	fun onMessage_invalidJson_sendsError() {
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		server.onMessage(conn, "not json")
		awaitSent(conn)

		assertThat(conn.sent.last()).contains("Invalid JSON")
	}

	@Test
	fun onMessage_serviceNotRunning_sendsError() {
		assertThat(MobileAccessibilityService.instance).isNull()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		server.onMessage(conn, """{"id":"2","command":"ping"}""")
		awaitSent(conn)

		assertThat(conn.sent.last()).contains("Accessibility service is not running")
	}

	@Test
	fun onMessage_validPing_stampsIdAndResponds() {
		// Route requires MobileAccessibilityService.instance to be set: build it + call connect.
		service = Robolectric.buildService(MobileAccessibilityService::class.java).create().get()
		MobileAccessibilityService::class.java.getDeclaredMethod("onServiceConnected")
			.apply { isAccessible = true }
			.invoke(service)
		assertThat(MobileAccessibilityService.instance).isNotNull()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		server.onMessage(conn, """{"id":"req-9","command":"ping"}""")
		awaitSent(conn)

		assertThat(conn.sent.last()).contains(""""id":"req-9"""")
		assertThat(conn.sent.last()).contains(""""pong":true""")
	}

	@Test
	fun onError_logsMessage() {
		server.onError(null, RuntimeException("boom"))

		assertThat(logs.any { it == "Error: boom" }).isTrue()
	}

	@Test
	fun onMessage_receiveFileBegin_opensOutputAndAcks() {
		injectInMemoryDownloadStream()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		server.onMessage(conn, """{"id":"b1","command":"receive_file_begin","params":{"path":"x.apk","size":10}}""")
		awaitSent(conn)

		val ack = conn.sent.last()
		assertThat(ack).contains(""""success":true""")
		assertThat(ack).contains(""""path":"x.apk"""")
		// Downloads collection (not the app-private external files dir) must hold the bytes.
		assertThat(server.downloads["x.apk"]).isNotNull()
	}

	@Test
	fun onMessage_binaryFrame_writesToOutput() {
		injectInMemoryDownloadStream()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())
		server.onMessage(conn, """{"id":"b2","command":"receive_file_begin","params":{"path":"x.bin","size":5}}""")
		awaitSent(conn)

		server.onMessage(conn, ByteBuffer.wrap("hello".toByteArray()))

		server.onMessage(conn, """{"id":"e2","command":"receive_file_end","params":{}}""")
		awaitSentCount(conn, 2)

		// Robolectric cannot round-trip MediaStore.openOutputStream (the framework's
		// ContentProvider authority check rejects the synthetic media provider), so the
		// stream source is injected; here we assert the streamed bytes landed in Downloads.
		val content = server.downloads["x.bin"]?.toString()
		assertThat(content).isEqualTo("hello")
		val endAck = conn.sent.last()
		assertThat(endAck).contains(""""success":true""")
		assertThat(endAck).contains(""""bytes":5""")
	}

	@Test
	fun onMessage_receiveFileEnd_sizeMismatch_sendsError() {
		injectInMemoryDownloadStream()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())
		server.onMessage(conn, """{"id":"b3","command":"receive_file_begin","params":{"path":"m.bin","size":100}}""")
		awaitSent(conn)
		server.onMessage(conn, ByteBuffer.wrap(ByteArray(5)))
		server.onMessage(conn, """{"id":"e3","command":"receive_file_end","params":{}}""")
		awaitSentCount(conn, 2)

		assertThat(conn.sent.last()).contains("size mismatch")
	}

	@Test
	fun onMessage_receiveFileEnd_apk_firesInstallIntent() {
		injectInMemoryDownloadStream()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())
		server.onMessage(conn, """{"id":"b4","command":"receive_file_begin","params":{"path":"y.apk","size":5}}""")
		awaitSent(conn)
		server.onMessage(conn, ByteBuffer.wrap(ByteArray(5)))
		server.onMessage(conn, """{"id":"e4","command":"receive_file_end","params":{}}""")
		awaitSentCount(conn, 2)

		val intent = shadowOf(RuntimeEnvironment.getApplication()).nextStartedActivity
		assertThat(intent).isNotNull()
		assertThat(intent.action).isEqualTo(Intent.ACTION_VIEW)
		assertThat(intent.type).isEqualTo("application/vnd.android.package-archive")
		// APK now installs from the MediaStore content uri, not a FileProvider file uri.
		assertThat(intent.data?.scheme).isEqualTo("content")
	}

	@Test
	fun onMessage_receiveFileEnd_nonApk_noInstallIntent() {
		injectInMemoryDownloadStream()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())
		server.onMessage(conn, """{"id":"b5","command":"receive_file_begin","params":{"path":"z.txt","size":3}}""")
		awaitSent(conn)
		server.onMessage(conn, ByteBuffer.wrap("abc".toByteArray()))
		server.onMessage(conn, """{"id":"e5","command":"receive_file_end","params":{}}""")
		awaitSentCount(conn, 2)

		val intent = shadowOf(RuntimeEnvironment.getApplication()).nextStartedActivity
		assertThat(intent == null || intent.action != Intent.ACTION_VIEW).isTrue()
	}

	@Test
	fun onMessage_receiveFileBegin_missingPath_sendsError() {
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		server.onMessage(conn, """{"id":"b6","command":"receive_file_begin","params":{}}""")
		awaitSent(conn)

		assertThat(conn.sent.last()).contains("path")
	}

	@Test
	fun onMessage_receiveFileEnd_withoutBegin_sendsError() {
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		server.onMessage(conn, """{"id":"e7","command":"receive_file_end","params":{}}""")
		awaitSent(conn)

		assertThat(conn.sent.last()).contains("active transfer")
	}

	@Test
	fun onClose_midTransfer_removesSession() {
		injectInMemoryDownloadStream()
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())
		server.onMessage(conn, """{"id":"b8","command":"receive_file_begin","params":{"path":"c.bin","size":4}}""")
		awaitSent(conn)

		// A dropped connection mid-transfer must free the session.
		server.onClose(conn, 1000, "bye", false)

		// After onClose, a fresh begin on the SAME conn must succeed (session cleared),
		// not report a stale transfer.
		server.onOpen(conn, FakeHandshake())
		server.onMessage(conn, """{"id":"b8b","command":"receive_file_begin","params":{"path":"c.bin","size":4}}""")
		awaitSent(conn)
		assertThat(conn.sent.last()).contains(""""success":true""")
	}

	@Test
	fun onMessage_receiveFileBegin_mediaStoreInsertFails_sendsError() {
		// Simulate contentResolver.insert returning null (e.g. download volume
		// unmounted). The begin handler must surface an error instead of
		// registering a transfer with a null stream.
		server.openDownloadStream = { _, _ -> null }
		val conn = FakeWebSocket()
		server.onOpen(conn, FakeHandshake())

		server.onMessage(conn, """{"id":"b9","command":"receive_file_begin","params":{"path":"d.bin","size":4}}""")
		awaitSent(conn)

		assertThat(conn.sent.last()).contains("MediaStore insert failed")
	}

	@Test
	fun mimeTypeFor_mapsExtensionsCorrectly() {
		assertThat(WebSocketCommandServer.mimeTypeFor("app.apk")).isEqualTo("application/vnd.android.package-archive")
		assertThat(WebSocketCommandServer.mimeTypeFor("APP.APK")).isEqualTo("application/vnd.android.package-archive")
		assertThat(WebSocketCommandServer.mimeTypeFor("note.txt")).isEqualTo("text/plain")
		assertThat(WebSocketCommandServer.mimeTypeFor("photo.jpg")).isEqualTo("image/jpeg")
		assertThat(WebSocketCommandServer.mimeTypeFor("photo.jpeg")).isEqualTo("image/jpeg")
		assertThat(WebSocketCommandServer.mimeTypeFor("icon.png")).isEqualTo("image/png")
		assertThat(WebSocketCommandServer.mimeTypeFor("data.bin")).isEqualTo("application/octet-stream")
		assertThat(WebSocketCommandServer.mimeTypeFor("noext")).isEqualTo("application/octet-stream")
	}

	@Test
	fun onStart_setsTimeoutAndLogs() {
		server.onStart()

		assertThat(logs.any { it.startsWith("Server started on port") }).isTrue()
		assertThat(server.connectionLostTimeout).isEqualTo(60)
	}

	private fun injectInMemoryDownloadStream() {
		// Robolectric's ShadowMediaProvider inserts a MediaStore row but openOutputStream
		// throws FileNotFoundException on the synthetic media authority, so the server's
		// stream source is replaced with an in-memory sink. Production keeps the real
		// MediaStore opener as the default. A synthetic content uri is returned so the
		// APK install intent still carries a content:// scheme.
		server.downloads.clear()
		server.openDownloadStream = { name, _ ->
			val stream = ByteArrayOutputStream().also { server.downloads[name] = it }
			WebSocketCommandServer.DownloadSink(stream, android.net.Uri.parse("content://media/external/downloads/$name"))
		}
	}

	private fun awaitSent(conn: FakeWebSocket, timeoutMs: Long = 3000) {
		// onMessage submits to a 4-thread pool and returns immediately; await the side effect.
		val start = System.currentTimeMillis()
		while (conn.sent.isEmpty() && System.currentTimeMillis() - start < timeoutMs) {
			Thread.sleep(20)
		}
		assertThat(conn.sent).isNotEmpty()
	}

	private fun awaitSentCount(conn: FakeWebSocket, expected: Int, timeoutMs: Long = 3000) {
		// Multi-step flows (begin + end) populate conn.sent more than once; the
		// plain awaitSent returns as soon as the first ack lands, so a follow-up
		// await after end would not actually wait for the end handler. Poll until
		// the expected number of messages has been sent.
		val start = System.currentTimeMillis()
		while (conn.sent.size < expected && System.currentTimeMillis() - start < timeoutMs) {
			Thread.sleep(20)
		}
		assertThat(conn.sent.size).isAtLeast(expected)
	}
}

private class FakeWebSocket : org.java_websocket.WebSocket {
	val sent = mutableListOf<String>()
	var openState = true
	var closed = false
	var closeCode: Int? = null
	private val remote = java.net.InetSocketAddress.createUnresolved("127.0.0.1", 12345)

	override fun send(s: String) { sent += s }
	override fun send(b: ByteArray) {}
	override fun send(b: java.nio.ByteBuffer) {}
	override fun sendFrame(f: org.java_websocket.framing.Framedata) {}
	override fun sendFrame(fs: Collection<org.java_websocket.framing.Framedata>) {}
	override fun sendFragmentedFrame(op: org.java_websocket.enums.Opcode, b: java.nio.ByteBuffer, fin: Boolean) {}
	override fun sendPing() {}
	override fun close() { closed = true; openState = false }
	override fun close(code: Int) { closed = true; openState = false; closeCode = code }
	override fun close(code: Int, reason: String) { closed = true; openState = false; closeCode = code }
	override fun closeConnection(code: Int, reason: String) { closed = true; openState = false; closeCode = code }
	override fun hasBufferedData() = false
	override fun isOpen() = openState
	override fun isClosing() = false
	override fun isClosed() = closed
	override fun isFlushAndClose() = false
	override fun getRemoteSocketAddress(): java.net.InetSocketAddress = remote
	override fun getLocalSocketAddress(): java.net.InetSocketAddress? = null
	// Draft is an abstract class; a null return is permitted by the contract.
	override fun getDraft(): org.java_websocket.drafts.Draft? = null
	override fun getReadyState(): org.java_websocket.enums.ReadyState = org.java_websocket.enums.ReadyState.OPEN
	override fun getResourceDescriptor(): String = "/"
	override fun hasSSLSupport() = false
	override fun getSSLSession(): javax.net.ssl.SSLSession = throw IllegalArgumentException("no ssl")
	override fun getProtocol(): org.java_websocket.protocols.IProtocol? = null
	override fun <T> setAttachment(t: T) {}
	override fun <T> getAttachment(): T? = null
}

private class FakeHandshake(val authHeader: String? = null) : org.java_websocket.handshake.ClientHandshake {
	override fun getFieldValue(name: String): String =
		if (name == "Authorization") authHeader ?: "" else ""

	override fun hasFieldValue(name: String): Boolean = name == "Authorization" && authHeader != null

	override fun iterateHttpFields(): Iterator<String> = listOf("Authorization").iterator()

	override fun getContent(): ByteArray = ByteArray(0)

	override fun getResourceDescriptor(): String = "/"
}
