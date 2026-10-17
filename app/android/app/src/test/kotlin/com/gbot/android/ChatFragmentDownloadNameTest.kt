package com.gbot.android

import com.google.common.truth.Truth.assertThat
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * downloadFileName names the in-process downloader's destination, so it must
 * survive percent-encoding, query strings and ':' (which is poison in a real
 * file name / MediaStore DISPLAY_NAME). Robolectric because the helper goes
 * through android.net.Uri, which is stubbed (throws) in plain JUnit.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class ChatFragmentDownloadNameTest {
	@Test
	fun percentEncodedName_isDecoded() {
		assertThat(ChatFragment.downloadFileName("/artifacts/a%20b.zip")).isEqualTo("a b.zip")
		assertThat(ChatFragment.downloadFileName("http://127.0.0.1:8765/artifacts/a%20b.zip"))
			.isEqualTo("a b.zip")
	}

	@Test
	fun nestedArtifactPath_keepsTheLeafOnly() {
		assertThat(ChatFragment.downloadFileName("/artifacts/lib/g.zip")).isEqualTo("g.zip")
	}

	@Test
	fun queryString_neverLeaksIntoTheName() {
		assertThat(ChatFragment.downloadFileName("http://127.0.0.1:8765/artifacts/g.zip?v=2"))
			.isEqualTo("g.zip")
	}

	@Test
	fun emptyUrl_fallsBackToTheGenericName() {
		assertThat(ChatFragment.downloadFileName("")).isEqualTo("gbot-download")
	}

	@Test
	fun trailingSlash_usesTheLastRealSegment() {
		// lastPathSegment skips the empty segment a trailing slash leaves
		// behind — the leaf must never be blank, a blank DISPLAY_NAME/file
		// name is what the fallback gbot-download exists for.
		assertThat(ChatFragment.downloadFileName("http://127.0.0.1:8765/artifacts/"))
			.isEqualTo("artifacts")
	}

	@Test
	fun colonInName_isStripped() {
		// ':' is poison in a real filesystem name / MediaStore DISPLAY_NAME —
		// keeping it would make the sink open fail or the row unusable.
		assertThat(ChatFragment.downloadFileName("http://127.0.0.1:8765/artifacts/12:30:00.zip"))
			.isEqualTo("123000.zip")
	}

	@Test
	fun colonRevealedByDecoding_isStrippedToo() {
		// Decoding happens before the strip, so an encoded colon cannot slip
		// through and poison the destination name.
		assertThat(ChatFragment.downloadFileName("http://127.0.0.1:8765/artifacts/%3Aa.zip"))
			.isEqualTo("a.zip")
	}
}
