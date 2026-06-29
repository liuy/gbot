package com.gbot.android

import android.provider.Settings
import android.view.View
import com.google.common.truth.Truth.assertThat
import com.gbot.android.databinding.ActivityMainBinding
import com.gbot.android.service.ConnectionForegroundService
import com.gbot.android.service.MobileAccessibilityService
import com.gbot.android.tunnel.SshTunnelConfig
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
class MainActivityTest {

	private lateinit var activity: MainActivity
	private lateinit var binding: ActivityMainBinding
	private var builtService: MobileAccessibilityService? = null

	@Before
	fun setup() {
		activity = Robolectric.buildActivity(MainActivity::class.java).create().resume().get()
		// MainActivity holds its own inflated binding; read it reflectively. Binding.bind(decorView)
		// fails because the root ScrollView is nested under the framework DecorView, not the decor itself.
		val field = MainActivity::class.java.getDeclaredField("binding")
		field.isAccessible = true
		@Suppress("UNCHECKED_CAST")
		binding = field.get(activity) as ActivityMainBinding
	}

	@After
	fun teardown() {
		builtService?.onDestroy()
		// MainActivity.onDestroy stops the server if running; ensure no static state leaks out.
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
	fun onCreate_setsDisconnectedStatus_andDefaultPort() {
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Disconnected")
		assertThat(binding.tvConnections.text.toString()).isEqualTo("0")
		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Start Server")
	}

	@Test
	fun onCreate_loadsSshConfig_defaultsIntoFields() {
		assertThat(binding.etServer.text.toString()).isEmpty()
		assertThat(binding.etSshPort.text.toString()).isEqualTo("22")
		assertThat(binding.etSshUser.text.toString()).isEmpty()
		assertThat(binding.etRemotePort.text.toString()).isEqualTo("8765")
		assertThat(binding.etLocalPort.text.toString()).isEqualTo("8765")
	}

	@Test
	fun onCreate_wifiTabSelected_sshGroupGone() {
		assertThat(binding.wifiConfigGroup.visibility).isEqualTo(View.VISIBLE)
		assertThat(binding.sshConfigGroup.visibility).isEqualTo(View.GONE)
	}

	// --- tabs ---

	@Test
	fun clickSshTab_switchesVisibility_andCurrentMode() {
		binding.tabSsh.performClick()

		assertThat(binding.sshConfigGroup.visibility).isEqualTo(View.VISIBLE)
		assertThat(binding.wifiConfigGroup.visibility).isEqualTo(View.GONE)
		// Empty SSH host routes to the disconnected string.
		assertThat(binding.tvIpAddress.text.toString()).isEqualTo("Disconnected")
	}

	@Test
	fun clickWifiTab_afterSsh_restoresWifiGroup() {
		binding.tabSsh.performClick()
		binding.tabWifi.performClick()

		assertThat(binding.wifiConfigGroup.visibility).isEqualTo(View.VISIBLE)
		assertThat(binding.sshConfigGroup.visibility).isEqualTo(View.GONE)
	}

	// --- SSH field persistence ---

	@Test
	fun sshFields_persistOnFocusChange() {
		binding.tabSsh.performClick()
		binding.etServer.setText("myhost")
		binding.etSshUser.setText("myuser")

		binding.etServer.onFocusChangeListener.onFocusChange(binding.etServer, false)

		val cfg = SshTunnelConfig.load(activity)
		assertThat(cfg.host).isEqualTo("myhost")
		assertThat(cfg.user).isEqualTo("myuser")
	}

	@Test
	fun readSshConfigFromFields_invalidPortString_fallsBackToDefault() {
		binding.tabSsh.performClick()
		binding.etSshPort.setText("abc")

		binding.etSshPort.onFocusChangeListener.onFocusChange(binding.etSshPort, false)

		assertThat(SshTunnelConfig.load(activity).port).isEqualTo(22)
	}

	@Test
	fun readSshConfigFromFields_invalidRemotePort_fallsBackToDefault() {
		binding.tabSsh.performClick()
		binding.etRemotePort.setText("nope")

		binding.etRemotePort.onFocusChangeListener.onFocusChange(binding.etRemotePort, false)

		assertThat(SshTunnelConfig.load(activity).remotePort).isEqualTo(8765)
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
		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Start Server")
	}

	@Test
	fun startServer_wifiMode_startsServerAndForegroundService() {
		setAccessibilityRunning()
		val localPort = freePort()
		binding.etPort.setText(localPort.toString())

		binding.btnToggleServer.performClick()

		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Stop Server")
		val serviceIntent = shadowOf(activity).nextStartedService
		assertThat(serviceIntent.component!!.className).isEqualTo(ConnectionForegroundService::class.java.name)
		assertThat(serviceIntent.getIntExtra(ConnectionForegroundService.EXTRA_PORT, -1)).isEqualTo(localPort)
		assertThat(serviceIntent.getBooleanExtra(ConnectionForegroundService.EXTRA_USE_SSH, true)).isFalse()
		assertThat(binding.etPort.isEnabled).isFalse()
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Waiting for connection…")
	}

	@Test
	fun startServer_sshMode_invalidConfig_logsError() {
		setAccessibilityRunning()
		binding.tabSsh.performClick()
		// Fields left empty -> invalid config.
		binding.btnToggleServer.performClick()

		assertThat(binding.tvLog.text.toString()).contains("Please fill Server, User, and Password")
		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Start Server")
	}

	@Test
	fun stopServer_afterStart_resetsUi() {
		setAccessibilityRunning()
		val localPort = freePort()
		binding.etPort.setText(localPort.toString())
		binding.btnToggleServer.performClick()

		binding.btnToggleServer.performClick()

		assertThat(binding.btnToggleServer.text.toString()).isEqualTo("Start Server")
		assertThat(binding.tvConnections.text.toString()).isEqualTo("0")
		assertThat(binding.etPort.isEnabled).isTrue()
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Disconnected")
		assertThat(binding.tvLog.text.toString()).contains("Server stopped")
	}

	// --- handleTunnelState (private, reflective) ---

	private fun invokeHandleTunnelState(state: SshTunnelState) {
		MainActivity::class.java.getDeclaredMethod("handleTunnelState", SshTunnelState::class.java)
			.apply { isAccessible = true }
			.invoke(activity, state)
	}

	@Test
	fun handleTunnelState_connected_setsWaitingWhenNoConnections() {
		invokeHandleTunnelState(SshTunnelState.Connected)

		assertThat(binding.tvLog.text.toString()).contains("SSH tunnel connected")
		// Connected arm sets WAITING when connections == 0 (no WebSocket client yet).
		assertThat(binding.tvStatus.text.toString()).isEqualTo("Waiting for connection…")
	}

	@Test
	fun handleTunnelState_connecting_setsWaiting() {
		invokeHandleTunnelState(SshTunnelState.Connecting)

		assertThat(binding.tvStatus.text.toString()).isEqualTo("Waiting for connection…")
	}

	@Test
	fun handleTunnelState_reconnecting_setsWaiting() {
		invokeHandleTunnelState(SshTunnelState.Reconnecting(5))

		assertThat(binding.tvStatus.text.toString()).isEqualTo("Waiting for connection…")
	}

	@Test
	fun handleTunnelState_stopped_logs() {
		invokeHandleTunnelState(SshTunnelState.Stopped)

		assertThat(binding.tvLog.text.toString()).contains("SSH tunnel disconnected")
	}

	@Test
	fun handleTunnelState_idle_noChange() {
		val before = binding.tvLog.text.toString()
		invokeHandleTunnelState(SshTunnelState.Idle)

		assertThat(binding.tvLog.text.toString()).isEqualTo(before)
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

	// --- getDeviceIpAddress / updateIPAddress ---

	@Test
	fun getDeviceIpAddress_returnsNull_whenNoWifi() {
		val ip = MainActivity::class.java.getDeclaredMethod("getDeviceIpAddress")
			.apply { isAccessible = true }
			.invoke(activity) as String?

		// Robolectric's WifiManager reports ipAddress == 0, which the code maps to null.
		assertThat(ip).isNull()
	}

	@Test
	fun updateIPAddress_wifiMode_showsNotConnectedWhenNoIp() {
		// WiFi is the default mode; onResume -> updateIPAddress with a null device IP.
		assertThat(binding.tvIpAddress.text.toString()).isEqualTo("Not connected to WiFi")
	}

	private fun freePort(): Int {
		// Bind and immediately release so the OS assigns a free port for the server to bind.
		return java.net.ServerSocket(0).use { it.localPort }
	}
}
