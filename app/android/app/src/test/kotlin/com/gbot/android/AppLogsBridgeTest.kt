package com.gbot.android

import com.google.common.truth.Truth.assertThat
import org.junit.Test

class AppLogsBridgeTest {

	private val bridge = AppLogsBridge()

	private fun resetBuffer() {
		GbotProcess.logBuffer.setLength(0)
	}

	@Test
	fun tail_returnsLastNLines() {
		resetBuffer()
		GbotProcess.logBuffer.append("alpha\nbeta\ngamma\n")
		assertThat(bridge.tail(2)).isEqualTo("beta\ngamma")
	}

	@Test
	fun tail_nBeyondLineCount_returnsEverything() {
		resetBuffer()
		GbotProcess.logBuffer.append("alpha\nbeta\n")
		assertThat(bridge.tail(500)).isEqualTo("alpha\nbeta")
	}

	@Test
	fun tail_nonPositiveN_returnsEmpty() {
		resetBuffer()
		GbotProcess.logBuffer.append("alpha\n")
		assertThat(bridge.tail(0)).isEmpty()
		assertThat(bridge.tail(-3)).isEmpty()
	}

	@Test
	fun tail_emptyBuffer_returnsEmpty() {
		resetBuffer()
		assertThat(bridge.tail(500)).isEmpty()
	}

	@Test
	fun clear_emptiesTheBuffer() {
		resetBuffer()
		GbotProcess.appendEvent("one")
		GbotProcess.appendEvent("two")
		bridge.clear()
		assertThat(GbotProcess.logBuffer.length).isEqualTo(0)
		assertThat(bridge.tail(500)).isEmpty()
	}
}
