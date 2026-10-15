package com.gbot.android

import com.google.common.truth.Truth.assertThat
import org.junit.Test

class ShareBridgeTest {

	private val pngBase64 =
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

	@Test
	fun decode_validPngDataUrl_returnsPngMagicBytes() {
		val bytes = decodeShareDataUrl("data:image/png;base64,$pngBase64")
		assertThat(bytes).isNotNull()
		assertThat(bytes!!.size).isGreaterThan(0)
		assertThat(bytes.sliceArray(0..3).toList())
			.isEqualTo(listOf(0x89.toByte(), 0x50.toByte(), 0x4E.toByte(), 0x47.toByte()))
	}

	@Test
	fun decode_jpegMime_returnsNull() {
		assertThat(decodeShareDataUrl("data:image/jpeg;base64,$pngBase64")).isNull()
	}

	@Test
	fun decode_missingBase64Separator_returnsNull() {
		assertThat(decodeShareDataUrl("data:image/png,$pngBase64")).isNull()
	}

	@Test
	fun decode_illegalBase64Characters_returnsNull() {
		assertThat(decodeShareDataUrl("data:image/png;base64,!!!")).isNull()
	}

	@Test
	fun decode_emptyString_returnsNull() {
		assertThat(decodeShareDataUrl("")).isNull()
	}

	@Test
	fun decode_emptyPayload_returnsNull() {
		assertThat(decodeShareDataUrl("data:image/png;base64,")).isNull()
	}

	@Test
	fun sanitize_pathTraversalSegments_dotsSurviveSlashesBecomeUnderscores() {
		val cleaned = sanitizeShareFileName("../../evil.png")
		assertThat(cleaned).doesNotContain("/")
		assertThat(cleaned.endsWith("evil.png")).isTrue()
	}

	@Test
	fun sanitize_mixedSeparators_allBecomeUnderscores() {
		assertThat(sanitizeShareFileName("a\\b/c.png")).isEqualTo("a_b_c.png")
	}

	@Test
	fun sanitize_emptyName_returnsFallback() {
		assertThat(sanitizeShareFileName("")).isEqualTo("gbot-share.png")
	}

	@Test
	fun sanitize_singleDot_returnsFallback() {
		assertThat(sanitizeShareFileName(".")).isEqualTo("gbot-share.png")
	}

	@Test
	fun sanitize_doubleDot_returnsFallback() {
		assertThat(sanitizeShareFileName("..")).isEqualTo("gbot-share.png")
	}

	@Test
	fun sanitize_overlongName_truncatesTo120() {
		assertThat(sanitizeShareFileName("a".repeat(200))).hasLength(120)
	}

	@Test
	fun sanitize_normalName_returnedUnchanged() {
		val name = "gbot-share-2026-10-15-105000.png"
		assertThat(sanitizeShareFileName(name)).isEqualTo(name)
	}
}
