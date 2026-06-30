package com.gbot.android

import android.provider.Settings
import android.view.View
import com.google.common.truth.Truth.assertThat
import com.gbot.android.databinding.ActivityMainBinding
import com.gbot.android.service.ConnectionForegroundService
import com.gbot.android.service.MobileAccessibilityService
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
class MainActivityTest {

	private lateinit var activity: MainActivity
	private lateinit var binding: ActivityMainBinding
	private var builtService: MobileAccessibilityService? = null

	@Before
	fun setup() {
		activity = Robolectric.buildActivity(MainActivity::class.java).create().resume().get()
		val field = MainActivity::class.java.getDeclaredField("binding")
		field.isAccessible = true
		@Suppress("UNCHECKED_CAST")
		binding = field.get(activity) as ActivityMainBinding
	}

	@After
	fun teardown() {
		builtService?.onDestroy()
	}

	private fun setAccessibilityRunning() {
		val svc = Robolectric.buildService(MobileAccessibilityService::class.java).create().get()
		MobileAccessibilityService::class.java.getDeclaredMethod("onServiceConnected")
			.apply { isAccessible = true }
			.invoke(svc)
		builtService = svc
	}

	// --- onCreate ---

	@Test
	fun onCreate_setsDisconnectedStatus_andConnectButtonLabel() {
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Disconnected")
		assertThat(binding.tvConnections.text.toString()).isEqualTo("0")
		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Connect")
	}

	@Test
	fun onCreate_showsServerConfigGroup() {
		assertThat(binding.serverConfigGroup.visibility).isEqualTo(View.VISIBLE)
	}

	// --- accessibility status ---

	@Test
	fun updateAccessibilityStatus_whenDisabled_showsButton() {
		assertThat(binding.btnOpenAccessibility.visibility).isEqualTo(View.VISIBLE)
		assertThat(binding.tvAccessibilityStatus.text.toString()).isEqualTo("Accessibility Service: OFF")
	}

	@Test
	fun btnOpenAccessibility_launchesSettingsIntent() {
		binding.btnOpenAccessibility.performClick()

		val next = shadowOf(activity).nextStartedActivity
		assertThat(next.action).isEqualTo(Settings.ACTION_ACCESSIBILITY_SETTINGS)
	}

	// --- startServer / stopServer ---

	@Test
	fun startServer_accessibilityNotRunning_logsError_doesNotStart() {
		assertThat(MobileAccessibilityService.isRunning).isFalse()

		binding.btnToggleServer.performClick()

		assertThat(binding.tvLog.text.toString()).contains("Accessibility service is not enabled!")
		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Connect")
	}

	@Test
	fun startServer_emptyHost_logsError_doesNotStart() {
		setAccessibilityRunning()
		// Host left empty — must not start.
		binding.btnToggleServer.performClick()

		assertThat(binding.tvLog.text.toString()).contains("gbot server host")
		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Connect")
	}

	@Test
	fun startServer_withHost_startsForegroundServiceWithHostAndPort() {
		setAccessibilityRunning()
		val port = freePort()
		binding.etServer.setText("192.168.1.10")
		binding.etPort.setText(port.toString())

		binding.btnToggleServer.performClick()

		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Disconnect")
		val serviceIntent = shadowOf(activity).nextStartedService
		assertThat(serviceIntent.component!!.className).isEqualTo(ConnectionForegroundService::class.java.name)
		assertThat(serviceIntent.getStringExtra(ConnectionForegroundService.EXTRA_HOST)).isEqualTo("192.168.1.10")
		assertThat(serviceIntent.getIntExtra(ConnectionForegroundService.EXTRA_PORT, -1)).isEqualTo(port)
		assertThat(binding.serverConfigGroup.visibility).isEqualTo(View.GONE)
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Waiting for connection…")
	}

	@Test
	fun stopServer_afterStart_resetsUi() {
		setAccessibilityRunning()
		binding.etServer.setText("127.0.0.1")
		binding.etPort.setText(freePort().toString())
		binding.btnToggleServer.performClick()

		binding.btnToggleServer.performClick()

		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Connect")
		assertThat(binding.tvConnections.text.toString()).isEqualTo("0")
		assertThat(binding.serverConfigGroup.visibility).isEqualTo(View.VISIBLE)
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Disconnected")
		assertThat(binding.tvLog.text.toString()).contains("Disconnected")
	}

	@Test
	fun connSink_connected_setsConnectedStatus() {
		setAccessibilityRunning()
		binding.etServer.setText("127.0.0.1")
		binding.etPort.setText(freePort().toString())
		binding.btnToggleServer.performClick()

		// Simulate the foreground service reporting an established connection.
		ConnectionForegroundService.connSink?.invoke(1)

		assertThat(binding.tvConnections.text.toString()).isEqualTo("1")
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Connected")
	}

	@Test
	fun connSink_disconnected_setsWaitingStatus() {
		setAccessibilityRunning()
		binding.etServer.setText("127.0.0.1")
		binding.etPort.setText(freePort().toString())
		binding.btnToggleServer.performClick()
		ConnectionForegroundService.connSink?.invoke(1)

		// Drop the connection — must go back to WAITING (still running).
		ConnectionForegroundService.connSink?.invoke(0)

		assertThat(binding.tvConnections.text.toString()).isEqualTo("0")
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Waiting for connection…")
	}

	// --- appendLog (private, reflective) ---

	@Test
	fun appendLog_truncatesAfter10000Chars() {
		val longMsg = "x".repeat(11000)
		MainActivity::class.java.getDeclaredMethod("appendLog", String::class.java)
			.apply { isAccessible = true }
			.invoke(activity, longMsg)

		assertThat(binding.tvLog.text.length).isEqualTo(5000)
	}

	private fun freePort(): Int {
		return java.net.ServerSocket(0).use { it.localPort }
	}
}
