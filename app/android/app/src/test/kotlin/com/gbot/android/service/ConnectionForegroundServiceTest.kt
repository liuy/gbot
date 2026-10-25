package com.gbot.android.service

import android.app.NotificationManager
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import com.gbot.android.GbotProcess
import com.google.common.truth.Truth.assertThat
import org.java_websocket.WebSocket
import org.java_websocket.handshake.ClientHandshake
import org.java_websocket.server.WebSocketServer
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.net.InetSocketAddress
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class ConnectionForegroundServiceTest {

	private lateinit var service: ConnectionForegroundService

	@Before
	fun setup() {
		GbotProcess.logBuffer.setLength(0)
		service = Robolectric.buildService(ConnectionForegroundService::class.java).create().get()
	}

	@After
	fun teardown() {
		service.onDestroy()
	}

	@Test
	fun onBind_returnsNull() {
		assertThat(service.onBind(null)).isNull()
	}

	@Test
	fun onCreate_createsNotificationChannel() {
		val nm = ApplicationProvider.getApplicationContext<android.content.Context>()
			.getSystemService(NotificationManager::class.java)
		val channel = nm.getNotificationChannel(ConnectionForegroundService.CHANNEL_ID)

		assertThat(channel).isNotNull()
		assertThat(channel!!.id).isEqualTo("gbot_server_channel")
		assertThat(channel.importance).isEqualTo(android.app.NotificationManager.IMPORTANCE_LOW)
	}

	@Test
	fun onStartCommand_startsForegroundWithHostAndPort_returnsSticky() {
		val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
			.putExtra(ConnectionForegroundService.EXTRA_HOST, "192.168.1.5")
			.putExtra(ConnectionForegroundService.EXTRA_PORT, 9999)

		val result = service.onStartCommand(intent, 0, 0)

		assertThat(result).isEqualTo(android.app.Service.START_STICKY)
		val nm = ApplicationProvider.getApplicationContext<android.content.Context>()
			.getSystemService(NotificationManager::class.java)
		val notification = shadowOf(nm).getNotification(ConnectionForegroundService.NOTIFICATION_ID)
		assertThat(notification).isNotNull()
		assertThat(notification!!.extras.getString(android.app.Notification.EXTRA_TITLE))
			.isEqualTo("GBot is running")
	}

	@Test
	fun onStartCommand_defaultPortIs8765_whenExtraMissing() {
		// onStartCommand must still start the client loop against the default port.
		val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
			.putExtra(ConnectionForegroundService.EXTRA_HOST, "127.0.0.1")

		val result = service.onStartCommand(intent, 0, 0)

		assertThat(result).isEqualTo(android.app.Service.START_STICKY)
	}

	@Test
	fun onStartCommand_appendsConnectAttemptToAppLogBuffer() {
		// Dial a port nothing is listening on so the connect fails fast and
		// the event lands in the app log buffer (the WUI panel's feed).
		val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
			.putExtra(ConnectionForegroundService.EXTRA_HOST, "127.0.0.1")
			.putExtra(ConnectionForegroundService.EXTRA_PORT, freeUnusedPort())
		service.onStartCommand(intent, 0, 0)

		val start = System.currentTimeMillis()
		while (System.currentTimeMillis() - start < 3000 &&
			!GbotProcess.logBuffer.toString().contains("Connect")
		) {
			Thread.sleep(20)
		}
		assertThat(GbotProcess.logBuffer.toString()).contains("Connect")
	}

	@Test
	fun onDestroy_stopsClientLoopWithoutThrowing() {
		val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
			.putExtra(ConnectionForegroundService.EXTRA_HOST, "127.0.0.1")
			.putExtra(ConnectionForegroundService.EXTRA_PORT, freeUnusedPort())
		service.onStartCommand(intent, 0, 0)

		// Must not throw even with an in-flight connect loop.
		service.onDestroy()
	}

	@Test
	fun onDestroy_closesLiveConnectionViaLoopFinally() {
		// Real loopback server pins the runInterruptible half of the zombie-dial
		// fix: with a bare awaitClose(), cancelling loopJob interrupts nothing,
		// the coroutine stays parked on closeLatch, and the finally-block
		// shutdown() — the single reclamation point for the socket — never runs,
		// so the server-side close below times out.
		val server = object : WebSocketServer(InetSocketAddress("127.0.0.1", 0)) {
			val started = CountDownLatch(1)
			val opened = CountDownLatch(1)
			val closed = CountDownLatch(1)
			override fun onStart() = started.countDown()
			override fun onOpen(conn: WebSocket, handshake: ClientHandshake) = opened.countDown()
			override fun onClose(conn: WebSocket, code: Int, reason: String?, remote: Boolean) = closed.countDown()
			override fun onMessage(conn: WebSocket, message: String) {}
			override fun onError(conn: WebSocket?, ex: Exception) {}
		}
		try {
			server.start()
			assertThat(server.started.await(10, TimeUnit.SECONDS)).isTrue()

			val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
				.putExtra(ConnectionForegroundService.EXTRA_HOST, "127.0.0.1")
				.putExtra(ConnectionForegroundService.EXTRA_PORT, server.port)
			service.onStartCommand(intent, 0, 0)

			// Once the handshake lands server-side the loop is on its way to
			// park in runInterruptible { awaitClose() } — but server onOpen
			// fires BEFORE the coroutine reaches awaitClose(), and destroying
			// in that gap exits via the !active guard, closing the socket
			// without ever exercising the interrupt path. Wait for the
			// client-side "Connected" log (client onOpen) plus a settle so the
			// loop is parked, then destroy from this exact state.
			assertThat(server.opened.await(10, TimeUnit.SECONDS)).isTrue()
			awaitLogContains("Connected to gbot")
			Thread.sleep(300)

			service.onDestroy()

			// The interrupt unwinds awaitClose() as a CancellationException, the
			// finally-block shutdown() closes the socket, and the server sees it.
			assertThat(server.closed.await(5, TimeUnit.SECONDS)).isTrue()
		} finally {
			server.stop(0)
		}
	}

	private fun freeUnusedPort(): Int {
		// Bind and immediately release so the OS assigns a free port that no WS
		// server listens on, forcing the client connect to fail/refuse.
		return java.net.ServerSocket(0).use { it.localPort }
	}

	private fun awaitLogContains(needle: String, timeoutMs: Long = 5000) {
		val start = System.currentTimeMillis()
		while (System.currentTimeMillis() - start < timeoutMs &&
			!GbotProcess.logBuffer.toString().contains(needle)
		) {
			Thread.sleep(20)
		}
		assertThat(GbotProcess.logBuffer.toString()).contains(needle)
	}
}
