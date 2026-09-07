package com.gbot.android

import androidx.test.ext.junit.runners.AndroidJUnit4
import com.google.common.truth.Truth.assertThat
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [33])
class GbotProcessTest {

	// Fakes a live daemon so stop()'s process?.let branch (and its log line)
	// actually executes; destroy() must stay a no-op under Robolectric.
	private class FakeProcess : Process() {
		override fun getOutputStream(): java.io.OutputStream = throw UnsupportedOperationException()
		override fun getInputStream(): java.io.InputStream = throw UnsupportedOperationException()
		override fun getErrorStream(): java.io.InputStream = throw UnsupportedOperationException()
		override fun waitFor(): Int = 0
		override fun exitValue(): Int = 0
		override fun destroy() {}
	}

	@Before
	fun setup() {
		GbotProcess.logBuffer.setLength(0)
	}

	@Test
	fun appendEvent_prefixesWallClockTimestampAndMessage() {
		GbotProcess.appendEvent("Stopping gbot")
		assertThat(GbotProcess.logBuffer.toString()).matches("\\[\\d{2}:\\d{2}:\\d{2}\\] Stopping gbot\\n")
	}

	@Test
	fun appendEvent_withTag_prefixesTimestampTagAndMessage() {
		GbotProcess.appendEvent("GbotProcess", "Stopping gbot")
		assertThat(GbotProcess.logBuffer.toString())
			.matches("\\[\\d{2}:\\d{2}:\\d{2}\\] GbotProcess: Stopping gbot\\n")
	}

	@Test
	fun appendEvent_truncatesOver10000Chars_keepsLast5000() {
		GbotProcess.appendEvent("x".repeat(11000))
		assertThat(GbotProcess.logBuffer.length).isEqualTo(5000)
	}

	@Test
	fun stop_withLiveProcess_appendsStopLineToLogBuffer() {
		GbotProcess::class.java.getDeclaredField("process").apply {
			isAccessible = true
		}.set(null, FakeProcess())

		GbotProcess.stop()

		assertThat(GbotProcess.logBuffer.toString())
			.matches("\\[\\d{2}:\\d{2}:\\d{2}\\] GbotProcess: Stopping gbot\\n")
	}

	@Test
	fun stop_withoutProcess_appendsNothing() {
		GbotProcess.stop()
		assertThat(GbotProcess.logBuffer.length).isEqualTo(0)
	}
}
