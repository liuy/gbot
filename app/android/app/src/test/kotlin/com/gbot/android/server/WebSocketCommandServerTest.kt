package com.gbot.android.server

import com.google.common.truth.Truth.assertThat
import com.gbot.android.service.MobileAccessibilityService
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class WebSocketCommandServerTest {

	private val logs = mutableListOf<String>()
	private val connCounts = mutableListOf<Int>()
	private val server = WebSocketCommandServer(
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
	fun onStart_setsTimeoutAndLogs() {
		server.onStart()

		assertThat(logs.any { it.startsWith("Server started on port") }).isTrue()
		assertThat(server.connectionLostTimeout).isEqualTo(60)
	}

	private fun awaitSent(conn: FakeWebSocket, timeoutMs: Long = 3000) {
		// onMessage submits to a 4-thread pool and returns immediately; await the side effect.
		val start = System.currentTimeMillis()
		while (conn.sent.isEmpty() && System.currentTimeMillis() - start < timeoutMs) {
			Thread.sleep(20)
		}
		assertThat(conn.sent).isNotEmpty()
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
