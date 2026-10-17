package com.gbot.android

import android.app.Notification
import android.app.NotificationManager
import android.webkit.WebView
import androidx.fragment.app.FragmentActivity
import androidx.test.core.app.ApplicationProvider
import com.google.common.truth.Truth.assertThat
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

/**
 * The download path exactly as the device runs it: WebView callback →
 * ArtifactDownloader.start, with no permission gate in between. These tests
 * pin the wiring seams that stay green through positional-argument slips and
 * detached-fragment races — through the REAL downloader, a real loopback
 * server and the real ContentResolver MediaStore path (fake provider behind
 * the media authority only).
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class ChatFragmentDownloadGateTest {

	private lateinit var activity: FragmentActivity
	private lateinit var fragment: ChatFragment
	private lateinit var nm: NotificationManager
	private lateinit var media: FakeMediaProvider

	@Before
	fun setUp() {
		GbotProcess.logBuffer.setLength(0)
		ArtifactDownloader.state.set(ArtifactDownloader.DownloadState.Idle)
		// Object statics outlive a test method — a renderer left registered by
		// another test class's service would post into this test's shadows.
		ArtifactDownloader.requestRender = null
		activity = Robolectric.buildActivity(FragmentActivity::class.java).setup().get()
		fragment = ChatFragment()
		activity.supportFragmentManager.beginTransaction().add(fragment, "chat").commitNow()
		nm = ApplicationProvider.getApplicationContext<android.content.Context>()
			.getSystemService(NotificationManager::class.java)
		media = installFakeMediaStore(activity)
	}

	@After
	fun tearDown() {
		// A task left on the worker thread would post into the next test's
		// freshly-reset shadows.
		assertThat(ArtifactDownloader.awaitIdle()).isTrue()
	}

	@Test
	fun listenerWiringHandsUrlAndMimeToTheDownloader() {
		val server = startServer(ByteArray(64))
		try {
			val url = server.url("artifacts/gate-x.zip")
			val webView = fragment.view?.findViewById<WebView>(R.id.webView)
			assertThat(webView).isNotNull()
			val listener = shadowOf(webView!!).downloadListener
			assertThat(listener).isNotNull()

			// onDownloadStart's five args are positional and the lambda ignores
			// three of them: a url/contentDisposition/mimeType swap keeps every
			// other test green while the device downloads the wrong thing.
			listener!!.onDownloadStart(
				url, "Mozilla/5.0", "attachment; filename=\"x.zip\"", "application/zip", -1L,
			)

			// Deterministic drain, not a timed poll — false means the download
			// never drained and this test must fail loudly.
			assertThat(ArtifactDownloader.awaitIdle()).isTrue()

			assertThat(server.requests.first()).contains("GET /artifacts/gate-x.zip")
			val inserts = insertStatementsFor(activity, "gate-x.zip")
			assertThat(inserts).hasSize(1)
			assertThat(inserts[0].contentValues.getAsString(android.provider.MediaStore.MediaColumns.MIME_TYPE))
				.isEqualTo("application/zip")
			val done = shadowOf(nm).allNotifications.filter {
				it.extras.getString(Notification.EXTRA_TITLE) == "Download complete"
			}
			assertThat(done).hasSize(1)
			assertThat(done[0].extras.getString(Notification.EXTRA_TEXT)).isEqualTo("Saved to Download/gbot/gate-x.zip")
		} finally {
			server.stop()
		}
	}

	@Test
	fun detachedFragmentDropsTheDownloadWithoutThrowing() {
		val server = startServer(ByteArray(64))
		try {
			val webView = fragment.view?.findViewById<WebView>(R.id.webView)
			assertThat(webView).isNotNull()
			val listener = shadowOf(webView!!).downloadListener
			assertThat(listener).isNotNull()
			activity.supportFragmentManager.beginTransaction().remove(fragment).commitNow()

			listener!!.onDownloadStart(
				server.url("artifacts/detached.zip"), "Mozilla/5.0",
				"attachment; filename=\"detached.zip\"", "application/zip", -1L,
			)

			assertThat(ArtifactDownloader.awaitIdle()).isTrue()
			// onWebViewDownload returns at `context ?: return` BEFORE calling
			// ArtifactDownloader.start, so nothing was ever submitted and the
			// drain no-op ran immediately.
			assertThat(server.accepts.get()).isEqualTo(0)
			assertThat(shadowOf(nm).allNotifications).isEmpty()
		} finally {
			server.stop()
		}
	}

	@Test
	fun detachedFragmentMidDownloadStillCompletesAndNotifies() {
		val server = startServer(ByteArray(64))
		try {
			val webView = fragment.view?.findViewById<WebView>(R.id.webView)
			assertThat(webView).isNotNull()
			val listener = shadowOf(webView!!).downloadListener
			assertThat(listener).isNotNull()
			listener!!.onDownloadStart(
				server.url("artifacts/mid.zip"), "Mozilla/5.0",
				"attachment; filename=\"mid.zip\"", "application/zip", -1L,
			)
			activity.supportFragmentManager.beginTransaction().remove(fragment).commitNow()

			assertThat(ArtifactDownloader.awaitIdle()).isTrue()

			// The download is application-scoped: a fragment-scoped owner (or a
			// viewLifecycleOwner scope) fails this test.
			val done = shadowOf(nm).allNotifications.filter {
				it.extras.getString(Notification.EXTRA_TITLE) == "Download complete"
			}
			assertThat(done).hasSize(1)
			assertThat(done[0].extras.getString(Notification.EXTRA_TEXT)).isEqualTo("Saved to Download/gbot/mid.zip")
			assertThat(media.streams.getValue(media.rows.single()).size()).isEqualTo(64)
		} finally {
			server.stop()
		}
	}
}
