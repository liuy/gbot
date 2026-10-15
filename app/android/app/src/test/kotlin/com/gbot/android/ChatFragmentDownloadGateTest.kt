package com.gbot.android

import android.app.DownloadManager
import android.content.Context
import android.webkit.WebView
import androidx.fragment.app.FragmentActivity
import com.google.common.truth.Truth.assertThat
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

/**
 * The download path exactly as the device runs it: WebView callback →
 * DownloadManager enqueue, with no permission gate in between — verified
 * on-device that the public-Downloads enqueue needs no storage permission,
 * so a prompt was pure interruption. These tests pin the wiring seams that
 * stay green through positional-argument slips and detached-fragment races.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class ChatFragmentDownloadGateTest {

	private lateinit var activity: FragmentActivity
	private lateinit var fragment: ChatFragment

	@Before
	fun setUp() {
		activity = Robolectric.buildActivity(FragmentActivity::class.java).setup().get()
		fragment = ChatFragment()
		activity.supportFragmentManager.beginTransaction().add(fragment, "chat").commitNow()
	}

	@Test
	fun enqueuesExactlyOneDownloadIntoDownloadsGbot() {
		fragment.onWebViewDownload(ARTIFACT_URL, MIME)

		assertThat(downloadShadow().requestCount).isEqualTo(1)
		val request = downloadShadow().getRequest(0)
		assertThat(request).isNotNull()
		assertThat(shadowOf(request).uri.toString()).isEqualTo(ARTIFACT_URL)
		assertThat(shadowOf(request).mimeType.toString()).isEqualTo(MIME)
		// The gbot/ subdirectory rides the leading slash of the destination
		// NAME — a bare "gbot" produced nameless "gbot-1" files.
		assertThat(shadowOf(request).destination.path).endsWith("/Download/gbot/x.zip")
	}

	@Test
	fun detachedFragmentDropsTheDownloadWithoutThrowing() {
		activity.supportFragmentManager.beginTransaction().remove(fragment).commitNow()

		fragment.onWebViewDownload(ARTIFACT_URL, MIME)

		assertThat(downloadShadow().requestCount).isEqualTo(0)
	}

	@Test
	fun listenerWiringHandsUrlAndMimeToTheEnqueue() {
		val webView = fragment.view?.findViewById<WebView>(R.id.webView)
		assertThat(webView).isNotNull()
		val listener = shadowOf(webView!!).downloadListener
		assertThat(listener).isNotNull()

		// onDownloadStart's five args are positional and the lambda ignores
		// three of them: a url/contentDisposition/mimeType swap keeps every
		// other test green while the device downloads the wrong thing.
		listener!!.onDownloadStart(
			ARTIFACT_URL, "Mozilla/5.0", "attachment; filename=\"x.zip\"", MIME, -1L,
		)

		assertThat(downloadShadow().requestCount).isEqualTo(1)
		val request = downloadShadow().getRequest(0)
		assertThat(shadowOf(request).uri.toString()).isEqualTo(ARTIFACT_URL)
		assertThat(shadowOf(request).mimeType.toString()).isEqualTo(MIME)
	}

	private fun downloadShadow() =
		shadowOf(activity.getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager)

	companion object {
		private const val ARTIFACT_URL = "http://127.0.0.1:8765/artifacts/x.zip"
		private const val MIME = "application/zip"
	}
}
