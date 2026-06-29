package com.gbot.android.service

import android.app.NotificationManager
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import com.google.common.truth.Truth.assertThat
import com.gbot.android.tunnel.SshTunnelState
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
		service = Robolectric.buildService(ConnectionForegroundService::class.java).create().get()
	}

	@After
	fun teardown() {
		// logSink/stateSink are static vars; clear them so they don't leak into other test classes.
		ConnectionForegroundService.logSink = null
		ConnectionForegroundService.stateSink = null
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
	fun onStartCommand_wifiMode_startsForegroundAndReturnsSticky() {
		val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
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
	fun onStartCommand_sshMode_emitsConnectingAndErrorLog() {
		ConnectionForegroundService.logSink = null
		ConnectionForegroundService.stateSink = null
		val logs = mutableListOf<String>()
		val states = mutableListOf<SshTunnelState>()
		ConnectionForegroundService.logSink = { logs.add(it) }
		ConnectionForegroundService.stateSink = { states.add(it) }

		val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
			.putExtra(ConnectionForegroundService.EXTRA_USE_SSH, true)

		service.onStartCommand(intent, 0, 0)

		// Empty SSH host fails fast; poll up to 2s for the Connecting state + error log.
		val start = System.currentTimeMillis()
		while (System.currentTimeMillis() - start < 2000 &&
			(!states.contains(SshTunnelState.Connecting) || logs.none { it.startsWith("SSH error:") })
		) {
			Thread.sleep(20)
		}
		assertThat(states).contains(SshTunnelState.Connecting)
		assertThat(logs.any { it.startsWith("SSH error:") }).isTrue()
	}

	@Test
	fun onDestroy_afterSshStart_emitsStoppedWithoutThrowing() {
		val states = mutableListOf<SshTunnelState>()
		ConnectionForegroundService.stateSink = { states.add(it) }

		val intent = Intent(ApplicationProvider.getApplicationContext(), ConnectionForegroundService::class.java)
			.putExtra(ConnectionForegroundService.EXTRA_USE_SSH, true)
		service.onStartCommand(intent, 0, 0)

		service.onDestroy()

		val start = System.currentTimeMillis()
		while (System.currentTimeMillis() - start < 2000 && !states.contains(SshTunnelState.Stopped)) {
			Thread.sleep(20)
		}
		assertThat(states).contains(SshTunnelState.Stopped)
	}
}
