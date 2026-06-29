package com.gbot.android.tunnel

import com.google.common.truth.Truth.assertThat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import org.junit.Test

class SshTunnelManagerTest {

	private fun makeManager(
		cfg: SshTunnelConfig = SshTunnelConfig(host = "127.0.0.1", user = "u", password = "p"),
		onLog: (String) -> Unit = {},
		onState: (SshTunnelState) -> Unit = {}
	): SshTunnelManager {
		val scope = CoroutineScope(Dispatchers.Unconfined)
		return SshTunnelManager(cfg, scope, onLog, onState)
	}

	@Test
	fun start_thenStop_emitsStoppedState() {
		val states = mutableListOf<SshTunnelState>()
		val mgr = makeManager(onState = { states.add(it) })

		mgr.start()
		mgr.stop()

		assertThat(states).contains(SshTunnelState.Stopped)
	}

	@Test
	fun stop_beforeStart_emitsStoppedState() {
		val states = mutableListOf<SshTunnelState>()
		val mgr = makeManager(onState = { states.add(it) })

		mgr.stop()

		assertThat(states).contains(SshTunnelState.Stopped)
	}

	@Test
	fun doubleStart_isIdempotent() {
		val states = mutableListOf<SshTunnelState>()
		val mgr = makeManager(onState = { states.add(it) })

		mgr.start()
		mgr.start()
		mgr.stop()

		val stoppedCount = states.count { it == SshTunnelState.Stopped }
		assertThat(stoppedCount).isEqualTo(1)
	}

	@Test
	fun start_emitsConnectingThenFailsAndReconnects() {
		val states = mutableListOf<SshTunnelState>()
		val logs = mutableListOf<String>()
		val mgr = SshTunnelManager(
			cfg = SshTunnelConfig(host = "127.0.0.1", port = 1, user = "u", password = "p"),
			scope = CoroutineScope(Dispatchers.Unconfined),
			onLog = { logs.add(it) },
			onState = { states.add(it) }
		)

		mgr.start()
		Thread.sleep(500)
		mgr.stop()

		assertThat(states).contains(SshTunnelState.Connecting)
		assertThat(states.any { it is SshTunnelState.Reconnecting }).isTrue()
		assertThat(logs.any { it.startsWith("SSH error:") }).isTrue()
	}

	@Test
	fun reconnection_backoffSequence_doubles() {
		val reconnectionWaits = mutableListOf<Int>()
		val mgr = SshTunnelManager(
			cfg = SshTunnelConfig(host = "127.0.0.1", port = 1, user = "u", password = "p"),
			scope = CoroutineScope(Dispatchers.Unconfined),
			onLog = {},
			onState = { state ->
				if (state is SshTunnelState.Reconnecting) {
					reconnectionWaits.add(state.waitSeconds)
				}
			}
		)

		mgr.start()
		Thread.sleep(3000)
		mgr.stop()

		assertThat(reconnectionWaits.size).isAtLeast(2)
		assertThat(reconnectionWaits[0]).isEqualTo(1)
		assertThat(reconnectionWaits[1]).isEqualTo(2)
	}

	@Test
	fun reconnection_backoffCappedAt60() {
		val reconnectionWaits = mutableListOf<Int>()
		val mgr = SshTunnelManager(
			cfg = SshTunnelConfig(host = "127.0.0.1", port = 1, user = "u", password = "p"),
			scope = CoroutineScope(Dispatchers.Unconfined),
			onLog = {},
			onState = { state ->
				if (state is SshTunnelState.Reconnecting) {
					reconnectionWaits.add(state.waitSeconds)
				}
			}
		)

		mgr.start()
		Thread.sleep(500)
		mgr.stop()

		assertThat(reconnectionWaits.all { it <= 60 }).isTrue()
	}
}

class SshTunnelStateTest {

	@Test
	fun idle_isObject() {
		assertThat(SshTunnelState.Idle).isSameInstanceAs(SshTunnelState.Idle)
	}

	@Test
	fun connecting_isObject() {
		assertThat(SshTunnelState.Connecting).isSameInstanceAs(SshTunnelState.Connecting)
	}

	@Test
	fun connected_isObject() {
		assertThat(SshTunnelState.Connected).isSameInstanceAs(SshTunnelState.Connected)
	}

	@Test
	fun stopped_isObject() {
		assertThat(SshTunnelState.Stopped).isSameInstanceAs(SshTunnelState.Stopped)
	}

	@Test
	fun reconnecting_holdsWaitSeconds() {
		val state = SshTunnelState.Reconnecting(30)
		assertThat(state.waitSeconds).isEqualTo(30)
	}

	@Test
	fun reconnecting_differentValues_notEqual() {
		val s1 = SshTunnelState.Reconnecting(1)
		val s2 = SshTunnelState.Reconnecting(2)
		assertThat(s1).isNotEqualTo(s2)
	}
}
