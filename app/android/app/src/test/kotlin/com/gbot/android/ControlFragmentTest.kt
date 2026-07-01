package com.gbot.android

import android.provider.Settings
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.google.common.truth.Truth.assertThat
import com.gbot.android.service.ConnectionForegroundService
import com.gbot.android.service.MobileAccessibilityService
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [33])
class ControlFragmentTest {

	private lateinit var activity: MainActivity
	private lateinit var fragment: ControlFragment
	private var builtService: MobileAccessibilityService? = null

	@Before
	fun setup() {
		activity = Robolectric.buildActivity(MainActivity::class.java)
			.create()
			.start()
			.resume()
			.get()

		// Swap in ControlFragment so its views are inflated and attached.
		fragment = ControlFragment()
		activity.supportFragmentManager
			.beginTransaction()
			.replace(R.id.fragmentContainer, fragment)
			.commitNow()
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

	private fun <T : View> findView(id: Int): T {
		return fragment.requireView().findViewById<T>(id)
			?: error("view $id not found in ControlFragment root")
	}

	private fun tvStatus() = findView<TextView>(R.id.tvStatus).text.toString()
	private fun tvIpAddress() = findView<TextView>(R.id.tvIpAddress).text.toString()
	private fun btnToggleText() = findView<Button>(R.id.btnToggleServer).text.toString()
	private fun serverConfigVisibility() = findView<LinearLayout>(R.id.serverConfigGroup).visibility
	private fun btnAccessibilityVisibility() = findView<Button>(R.id.btnOpenAccessibility).visibility
	private fun tvAccessibilityText() = findView<TextView>(R.id.tvAccessibilityStatus).text.toString()
	private fun tvLogText() = findView<TextView>(R.id.tvLog).text.toString()

	private fun clickToggle() = findView<Button>(R.id.btnToggleServer).performClick()
	private fun clickAccessibility() = findView<Button>(R.id.btnOpenAccessibility).performClick()

	private fun setHostPort(host: String, port: Int) {
		findView<EditText>(R.id.etServer).setText(host)
		findView<EditText>(R.id.etPort).setText(port.toString())
	}

	@Test
	fun onCreate_setsDisconnectedStatus_andConnectButtonLabel() {
		assertThat(tvStatus()).isEqualTo("Disconnected")
		assertThat(tvIpAddress()).isEqualTo("--")
		assertThat(btnToggleText()).isEqualTo("Connect")
	}

	@Test
	fun onCreate_showsServerConfigGroup() {
		assertThat(serverConfigVisibility()).isEqualTo(View.VISIBLE)
	}

	@Test
	fun updateAccessibilityStatus_whenDisabled_showsButton() {
		assertThat(btnAccessibilityVisibility()).isEqualTo(View.VISIBLE)
		assertThat(tvAccessibilityText()).isEqualTo("Accessibility Service: OFF")
	}

	@Test
	fun btnOpenAccessibility_launchesSettingsIntent() {
		clickAccessibility()
		val next = shadowOf(activity).nextStartedActivity
		assertThat(next.action).isEqualTo(Settings.ACTION_ACCESSIBILITY_SETTINGS)
	}

	@Test
	fun startServer_accessibilityNotRunning_logsError_doesNotStart() {
		assertThat(MobileAccessibilityService.isRunning).isFalse()

		clickToggle()

		assertThat(tvLogText()).contains("Accessibility service is not enabled!")
		assertThat(btnToggleText()).isEqualTo("Connect")
	}

	@Test
	fun startServer_emptyHost_logsError_doesNotStart() {
		setAccessibilityRunning()
		clickToggle()

		assertThat(tvLogText()).contains("gbot server host")
		assertThat(btnToggleText()).isEqualTo("Connect")
	}

	@Test
	fun startServer_withHost_startsForegroundServiceWithHostAndPort() {
		setAccessibilityRunning()
		val port = freePort()
		setHostPort("192.168.1.10", port)

		clickToggle()

		assertThat(btnToggleText()).isEqualTo("Disconnect")
		val serviceIntent = shadowOf(activity).nextStartedService
		assertThat(serviceIntent.component!!.className).isEqualTo(ConnectionForegroundService::class.java.name)
		assertThat(serviceIntent.getStringExtra(ConnectionForegroundService.EXTRA_HOST)).isEqualTo("192.168.1.10")
		assertThat(serviceIntent.getIntExtra(ConnectionForegroundService.EXTRA_PORT, -1)).isEqualTo(port)
		assertThat(serverConfigVisibility()).isEqualTo(View.GONE)
		assertThat(tvStatus()).isEqualTo("Waiting for connection…")
	}

	@Test
	fun stopServer_afterStart_resetsUi() {
		setAccessibilityRunning()
		setHostPort("127.0.0.1", freePort())
		clickToggle()

		clickToggle()

		assertThat(btnToggleText()).isEqualTo("Connect")
		assertThat(tvIpAddress()).isEqualTo("--")
		assertThat(serverConfigVisibility()).isEqualTo(View.VISIBLE)
		assertThat(tvStatus()).isEqualTo("Disconnected")
		assertThat(tvLogText()).contains("Disconnected")
	}

	@Test
	fun connSink_connected_setsConnectedStatusAndHost() {
		setAccessibilityRunning()
		setHostPort("127.0.0.1", freePort())
		clickToggle()

		ConnectionForegroundService.connSink?.invoke(1, "127.0.0.1:8765")

		assertThat(tvIpAddress()).isEqualTo("127.0.0.1:8765")
		assertThat(tvStatus()).isEqualTo("Connected")
	}

	@Test
	fun connSink_disconnected_setsWaitingStatus() {
		setAccessibilityRunning()
		setHostPort("127.0.0.1", freePort())
		clickToggle()
		ConnectionForegroundService.connSink?.invoke(1, "127.0.0.1:8765")

		ConnectionForegroundService.connSink?.invoke(0, "127.0.0.1:8765")

		assertThat(tvStatus()).isEqualTo("Waiting for connection…")
	}

	@Test
	fun appendLog_truncatesAfter10000Chars() {
		val longMsg = "x".repeat(11000)
		ControlFragment::class.java.getDeclaredMethod("appendLog", String::class.java)
			.apply { isAccessible = true }
			.invoke(fragment, longMsg)
		assertThat(tvLogText().length).isEqualTo(5000)
	}

	private fun freePort(): Int {
		return java.net.ServerSocket(0).use { it.localPort }
	}
}
