package com.gbot.android.service

import com.google.common.truth.Truth.assertThat
import org.junit.Test

// Plain JVM test — no Robolectric, whose native binder does not load on
// arm64 Termux JVMs. Pins the empty-host regression: a missing extra once
// produced ws://:8765, which the ws scheme resolved to port 80 on ::1.
class ConnectionForegroundServiceTargetTest {

	@Test
	fun resolveTarget_fallsBackToLoopbackDefaults_whenIntentIsNull() {
		assertThat(ConnectionForegroundService.resolveTarget(null))
			.isEqualTo(ConnectionForegroundService.DEFAULT_HOST to ConnectionForegroundService.DEFAULT_PORT)
	}
}
