package com.gbot.android.service

import android.app.NotificationManager
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import com.google.common.truth.Truth.assertThat
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class ConnectionForegroundServiceTest {

	private lateinit var service: ConnectionForegroundService

	@Before
	fun setup() {
		ConnectionForegroundService.logSink = null
		ConnectionForegroundService.connSink = null
		service = Robolectric.buildService(ConnectionForegroundService::class.java).create().get()
	}

	@After
	fun teardown() {
		// logSink/connSink are static vars; clear them so they don't leak into other test classes.
		ConnectionForegroundService.logSink = null
		ConnectionForegroundService.connSink = null
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
	fun onStartCommand_emitsConnectAttemptToLogSink() {
		val logs = mutableListOf<String>()
		ConnectionForegroundService.logSink = { logs.add(it) }

		// Dial a port nothing is listening on so the connect fails fast and logs.
		val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
			.putExtra(ConnectionForegroundService.EXTRA_HOST, "127.0.0.1")
			.putExtra(ConnectionForegroundService.EXTRA_PORT, freeUnusedPort())
		service.onStartCommand(intent, 0, 0)

		// Poll up to 3s for a connect-failed/refused log line.
		val start = System.currentTimeMillis()
		while (System.currentTimeMillis() - start < 3000 &&
			logs.none { it.contains("Connect") }
		) {
			Thread.sleep(20)
		}
		assertThat(logs.any { it.contains("Connect") }).isTrue()
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

	private fun freeUnusedPort(): Int {
		// Bind and immediately release so the OS assigns a free port that no WS
		// server listens on, forcing the client connect to fail/refuse.
		return java.net.ServerSocket(0).use { it.localPort }
	}
}
