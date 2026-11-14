package com.gbot.android

import android.content.Intent
import android.net.Uri
import com.google.common.truth.Truth.assertThat
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * buildShareTextSendIntent assembles an ACTION_SEND whose pieces are
 * android.jar classes — stubbed (throwing) under plain JUnit, so this rides
 * Robolectric like ChatFragmentDownloadNameTest.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class ShareTextIntentTest {
	@Test
	fun sendIntent_carriesStreamUriMarkdownMimeAndReadGrant() {
		val uri = Uri.parse("content://com.termux.fileprovider/share/a.md")
		val intent = buildShareTextSendIntent(uri)
		assertThat(intent.action).isEqualTo(Intent.ACTION_SEND)
		assertThat(intent.type).isEqualTo("text/markdown")
		assertThat(intent.extras?.get(Intent.EXTRA_STREAM)).isEqualTo(uri)
		assertThat(intent.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION)
			.isNotEqualTo(0)
	}
}
