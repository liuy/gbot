package com.gbot.android

import com.google.common.truth.Truth.assertThat
import org.junit.Test

/**
 * Pure logic for the WUI target switch (local daemon vs user-configured
 * remote daemon). Companion-level like ConnectionForegroundService's
 * resolveTarget — plain JUnit, no Robolectric (its native binder does not
 * load on arm64 Termux JVMs).
 */
class ChatFragmentTargetUrlTest {
	@Test
	fun localTarget_buildsTheLocalDaemonUrl() {
		assertThat(ChatFragment.buildTargetUrl("local", "", 8765)).isEqualTo("http://127.0.0.1:8765/")
	}

	@Test
	fun localTarget_ignoresRemoteConfigEvenWhenPresent() {
		assertThat(ChatFragment.buildTargetUrl("local", "nas.box", 9000))
			.isEqualTo("http://127.0.0.1:8765/")
	}

	@Test
	fun remoteTarget_buildsHostPortUrl() {
		assertThat(ChatFragment.buildTargetUrl("remote", "192.168.1.20", 9000))
			.isEqualTo("http://192.168.1.20:9000/")
		assertThat(ChatFragment.buildTargetUrl("remote", "nas.box.local", 8765))
			.isEqualTo("http://nas.box.local:8765/")
	}

	@Test
	fun remoteTarget_blankHost_degradesToTheLocalUrl() {
		assertThat(ChatFragment.buildTargetUrl("remote", "", 8765)).isEqualTo("http://127.0.0.1:8765/")
		assertThat(ChatFragment.buildTargetUrl("remote", "   ", 8765)).isEqualTo("http://127.0.0.1:8765/")
	}

	@Test
	fun unknownTarget_degradesToTheLocalUrl() {
		assertThat(ChatFragment.buildTargetUrl("bogus", "nas.box", 9000))
			.isEqualTo("http://127.0.0.1:8765/")
	}
}
