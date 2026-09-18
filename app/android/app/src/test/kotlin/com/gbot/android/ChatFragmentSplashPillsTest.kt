package com.gbot.android

import com.google.common.truth.Truth.assertThat
import org.junit.Test

/**
 * Pure logic for the splash endpoint picker: the rendered pill list
 * (splashPillSpecs) and each pill's visual state (splashPillState) over the
 * connection phase. Companion-level like parseRemoteTargets — plain JUnit,
 * no Robolectric (its native binder does not load on arm64 Termux JVMs).
 * Localized labels are passed IN as plain strings so both functions stay
 * JVM-only.
 */
class ChatFragmentSplashPillsTest {
	private val desktop = ChatFragment.RemoteTarget("桌面", "192.168.1.5", 8765)
	private val nas = ChatFragment.RemoteTarget("nas", "nas.box.local", 9000)

	// -------------------------------------------------------- splashPillSpecs

	@Test
	fun specs_zeroRemotes_yieldsEmptyList_noPillGroup() {
		assertThat(ChatFragment.splashPillSpecs(emptyList(), "Local", "this device")).isEmpty()
	}

	@Test
	fun specs_localPillAlwaysFirst() {
		val specs = ChatFragment.splashPillSpecs(listOf(nas), "Local", "this device")
		assertThat(specs).hasSize(2)
		assertThat(specs[0].key).isEqualTo(ChatFragment.TARGET_LOCAL)
		assertThat(specs[0].isLocal).isTrue()
		assertThat(specs[0].name).isEqualTo("Local")
		assertThat(specs[0].address).isEqualTo("this device")
	}

	@Test
	fun specs_remotesFollowInStoredOrder() {
		val specs = ChatFragment.splashPillSpecs(
			listOf(desktop, nas, ChatFragment.RemoteTarget("pi", "pi.lan", 22)),
			"Local", "this device",
		)
		assertThat(specs).hasSize(4)
		assertThat(specs.map { it.key })
			.isEqualTo(listOf(ChatFragment.TARGET_LOCAL, "桌面", "nas", "pi"))
		assertThat(specs.drop(1).all { !it.isLocal }).isTrue()
	}

	@Test
	fun specs_remoteAddressIsHostColonPort() {
		assertThat(ChatFragment.splashPillSpecs(listOf(nas), "Local", "this device")[1].address)
			.isEqualTo("nas.box.local:9000")
		val port80 = ChatFragment.splashPillSpecs(
			listOf(ChatFragment.RemoteTarget("nas", "nas.box.local", 80)), "Local", "this device",
		)
		assertThat(port80[1].address).isEqualTo("nas.box.local:80")
	}

	@Test
	fun specs_preservesUnicodeAndSpaceNames() {
		val spacey = ChatFragment.splashPillSpecs(
			listOf(ChatFragment.RemoteTarget("my nas", "nas.box", 1)), "Local", "this device",
		)
		assertThat(spacey[1].name).isEqualTo("my nas")
		val zh = ChatFragment.splashPillSpecs(listOf(desktop), "Local", "this device")
		assertThat(zh[1].name).isEqualTo("桌面")
	}

	// ---------------------------------------------- prefs JSON → specs pipeline

	@Test
	fun specsFromPrefsJson_integrationPipeline() {
		// The exact JSON shape NativeThemeBridge.getRemoteTargets serves /
		// setRemoteTargets stores — the prefs→UI-model assembly path.
		val remotes = ChatFragment.parseRemoteTargets(
			"""[{"name":"桌面","host":"192.168.1.5","port":8765}]"""
		)
		val specs = ChatFragment.splashPillSpecs(remotes, "Local", "this device")
		assertThat(specs).hasSize(2)
		assertThat(specs[0].key).isEqualTo(ChatFragment.TARGET_LOCAL)
		assertThat(specs[0].isLocal).isTrue()
		assertThat(specs[1].key).isEqualTo("桌面")
		assertThat(specs[1].isLocal).isFalse()
		assertThat(specs[1].address).isEqualTo("192.168.1.5:8765")
	}

	// ---------------------------------------------------------- splashPillState

	@Test
	fun state_nonActivePill_idle() {
		assertThat(
			ChatFragment.splashPillState(
				"nas", ChatFragment.TARGET_LOCAL, "nas", ChatFragment.SplashPhase.CONNECTING,
			)
		).isEqualTo(ChatFragment.SplashPillState.IDLE)
		assertThat(
			ChatFragment.splashPillState(
				ChatFragment.TARGET_LOCAL, ChatFragment.TARGET_REMOTE, "nas", ChatFragment.SplashPhase.LIVE,
			)
		).isEqualTo(ChatFragment.SplashPillState.IDLE)
	}

	@Test
	fun state_activeKeyIsCurrentNameWhenRemote() {
		assertThat(
			ChatFragment.splashPillState(
				"nas", ChatFragment.TARGET_REMOTE, "nas", ChatFragment.SplashPhase.CONNECTING,
			)
		).isEqualTo(ChatFragment.SplashPillState.CONNECTING)
		assertThat(
			ChatFragment.splashPillState(
				"nas", ChatFragment.TARGET_REMOTE, "nas", ChatFragment.SplashPhase.LIVE,
			)
		).isEqualTo(ChatFragment.SplashPillState.ACTIVE)
		assertThat(
			ChatFragment.splashPillState(
				"nas", ChatFragment.TARGET_REMOTE, "nas", ChatFragment.SplashPhase.FAILED,
			)
		).isEqualTo(ChatFragment.SplashPillState.FAILED)
	}

	@Test
	fun state_activeKeyIsLocalWhenLocal() {
		assertThat(
			ChatFragment.splashPillState(
				ChatFragment.TARGET_LOCAL, ChatFragment.TARGET_LOCAL, "", ChatFragment.SplashPhase.CONNECTING,
			)
		).isEqualTo(ChatFragment.SplashPillState.CONNECTING)
		assertThat(
			ChatFragment.splashPillState(
				ChatFragment.TARGET_LOCAL, ChatFragment.TARGET_LOCAL, "", ChatFragment.SplashPhase.LIVE,
			)
		).isEqualTo(ChatFragment.SplashPillState.ACTIVE)
		assertThat(
			ChatFragment.splashPillState(
				ChatFragment.TARGET_LOCAL, ChatFragment.TARGET_LOCAL, "", ChatFragment.SplashPhase.FAILED,
			)
		).isEqualTo(ChatFragment.SplashPillState.FAILED)
	}

	@Test
	fun state_staleCurrent_selectsNothing() {
		// A stale wui_current that matches no pill selects nothing — no pill
		// shows the failure state (mirrors buildTargetUrl's degrade-to-local).
		assertThat(
			ChatFragment.splashPillState(
				"nas", ChatFragment.TARGET_REMOTE, "gone", ChatFragment.SplashPhase.FAILED,
			)
		).isEqualTo(ChatFragment.SplashPillState.IDLE)
	}

	@Test
	fun state_totalFunction_unmatchedKeyStillReturns() {
		// Total function: unmatched keys still return a state, never crash.
		assertThat(
			ChatFragment.splashPillState(
				ChatFragment.TARGET_LOCAL, ChatFragment.TARGET_LOCAL, "", ChatFragment.SplashPhase.FAILED,
			)
		).isEqualTo(ChatFragment.SplashPillState.FAILED)
	}
}
