package com.gbot.android.service

import android.app.Notification
import android.app.NotificationManager
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import com.gbot.android.ArtifactDownloader
import com.gbot.android.FakeMediaProvider
import com.gbot.android.GbotProcess
import com.gbot.android.installFakeMediaStore
import com.gbot.android.startServer
import com.google.common.truth.Truth.assertThat
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.util.concurrent.atomic.AtomicReference

/**
 * Download progress rides the service's existing notification (id 1001): the
 * downloader only publishes [ArtifactDownloader.state], the service synthesizes
 * the content from (connection state, download state) in ONE function and is
 * the exclusive renderer. These tests drive the real downloader against a real
 * loopback server with the real service registered as renderer.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class ConnectionForegroundServiceDownloadRenderTest {

	private lateinit var service: ConnectionForegroundService
	private lateinit var ctx: android.content.Context
	private lateinit var nm: NotificationManager
	private lateinit var media: FakeMediaProvider

	@Before
	fun setUp() {
		ctx = ApplicationProvider.getApplicationContext()
		GbotProcess.logBuffer.setLength(0)
		ArtifactDownloader.state.set(ArtifactDownloader.DownloadState.Idle)
		nm = ctx.getSystemService(NotificationManager::class.java)
		media = installFakeMediaStore(ctx)
		service = Robolectric.buildService(ConnectionForegroundService::class.java).create().get()
	}

	@After
	fun tearDown() {
		// A test may have replaced the service's own hook with a wrapper.
		ArtifactDownloader.requestRender = null
		assertThat(ArtifactDownloader.awaitIdle()).isTrue()
		service.onDestroy()
	}

	private fun startServiceOn(port: Int) {
		val intent = Intent(ctx, ConnectionForegroundService::class.java)
			.putExtra(ConnectionForegroundService.EXTRA_HOST, "127.0.0.1")
			.putExtra(ConnectionForegroundService.EXTRA_PORT, port)
		service.onStartCommand(intent, 0, 0)
	}

	@Test
	fun downloadingState_showsNameAndProgressInTheServiceNotification() {
		val port = freeUnusedPort()
		startServiceOn(port)
		val body = ByteArray(4096) { (it % 251).toByte() }
		val server = startServer(body)
		try {
			val midRun = AtomicReference<Notification?>(null)
			val midState = AtomicReference<ArtifactDownloader.DownloadState.Downloading?>(null)
			// Same behavior as the service's own hook (renderStatusNotification
			// is the service's real update path) plus a capture of what was
			// actually posted while the state was Downloading. The first chunk
			// always renders: run() resets the progress throttle to 0.
			ArtifactDownloader.requestRender = {
				service.renderStatusNotification()
				val downloading = ArtifactDownloader.state.get()
					as? ArtifactDownloader.DownloadState.Downloading
				if (downloading != null) {
					midState.set(downloading)
					midRun.set(shadowOf(nm).getNotification(ConnectionForegroundService.NOTIFICATION_ID))
				}
			}

			val reason = ArtifactDownloader.run(ctx, server.url("dl-live.zip"), "dl-live.zip", null, 0)

			assertThat(reason).isNull()
			val notification = midRun.get()
			assertThat(notification)
				.isNotNull()
			val extras = notification!!.extras
			assertThat(extras.getString(Notification.EXTRA_TITLE)).isEqualTo("downloading dl-live.zip")
			assertThat(extras.getString(Notification.EXTRA_TEXT))
				.isEqualTo("Connected to gbot on port $port")
			// The posted bar mirrors the published state exactly. bytesDone is
			// >= 1: if pump stopped reporting progress, no mid-run render with
			// a positive count could ever be captured.
			val snapshot = midState.get()
			assertThat(snapshot).isNotNull()
			assertThat(snapshot!!.name).isEqualTo("dl-live.zip")
			assertThat(snapshot.bytesTotal).isEqualTo(4096L)
			assertThat(snapshot.bytesDone).isIn(1L..4096L)
			assertThat(extras.getInt(Notification.EXTRA_PROGRESS_MAX))
				.isEqualTo(snapshot.bytesTotal.toInt())
			assertThat(extras.getInt(Notification.EXTRA_PROGRESS))
				.isEqualTo(snapshot.bytesDone.toInt())
			assertThat(extras.getBoolean(Notification.EXTRA_PROGRESS_INDETERMINATE)).isFalse()
		} finally {
			server.stop()
		}
	}

	@Test
	fun finishedState_restoresTheConnectionNotification() {
		val port = freeUnusedPort()
		startServiceOn(port)
		val server = startServer(ByteArray(64))
		try {
			// The service's own hook (registered in onCreate) does the renders.
			val reason = ArtifactDownloader.run(ctx, server.url("dl-done.zip"), "dl-done.zip", null, 0)

			assertThat(reason).isNull()

			val notification = shadowOf(nm).getNotification(ConnectionForegroundService.NOTIFICATION_ID)
			assertThat(notification).isNotNull()
			assertThat(notification!!.extras.getString(Notification.EXTRA_TITLE))
				.isEqualTo("GBot is running")
			assertThat(notification.extras.getString(Notification.EXTRA_TEXT))
				.isEqualTo("Connected to gbot on port $port")
			// No progress bar survives the download.
			assertThat(notification.extras.getInt(Notification.EXTRA_PROGRESS)).isEqualTo(0)
			assertThat(notification.extras.getInt(Notification.EXTRA_PROGRESS_MAX)).isEqualTo(0)
		} finally {
			server.stop()
		}
	}

	@Test
	fun buildStatusNotification_unknownTotalRendersIndeterminateProgress() {
		val notification = service.buildStatusNotification(
			"127.0.0.1",
			8765,
			ArtifactDownloader.DownloadState.Downloading("big.bin", 1000, -1, 123L),
		)

		assertThat(notification.extras.getString(Notification.EXTRA_TITLE)).isEqualTo("downloading big.bin")
		assertThat(notification.extras.getBoolean(Notification.EXTRA_PROGRESS_INDETERMINATE)).isTrue()
		// A negative max would be nonsense on screen — unknown clamps to 0.
		assertThat(notification.extras.getInt(Notification.EXTRA_PROGRESS_MAX)).isEqualTo(0)
		assertThat(notification.extras.getInt(Notification.EXTRA_PROGRESS)).isEqualTo(1000)
	}

	@Test
	fun buildStatusNotification_idleAndFinishedRenderTheConnectionForm() {
		val idle = service.buildStatusNotification("127.0.0.1", 8765, ArtifactDownloader.DownloadState.Idle)
		assertThat(idle.extras.getString(Notification.EXTRA_TITLE)).isEqualTo("GBot is running")
		assertThat(idle.extras.getString(Notification.EXTRA_TEXT))
			.isEqualTo("Connected to gbot on port 8765")
		assertThat(idle.extras.getInt(Notification.EXTRA_PROGRESS_MAX)).isEqualTo(0)

		val finished = service.buildStatusNotification("127.0.0.1", 8765, ArtifactDownloader.DownloadState.Finished)
		assertThat(finished.extras.getString(Notification.EXTRA_TITLE)).isEqualTo("GBot is running")
		assertThat(finished.extras.getInt(Notification.EXTRA_PROGRESS_MAX)).isEqualTo(0)
	}

	@Test
	fun buildStatusNotification_originHostRendersTheOriginNotThePort() {
		// The port extra is meaningless for an origin host — naming it would
		// point the user at a port the connection does not use.
		val idle = service.buildStatusNotification("https://machine.ts.net", 8765, ArtifactDownloader.DownloadState.Idle)
		assertThat(idle.extras.getString(Notification.EXTRA_TEXT))
			.isEqualTo("Connected to gbot at https://machine.ts.net")

		val downloading = service.buildStatusNotification(
			"https://machine.ts.net",
			8765,
			ArtifactDownloader.DownloadState.Downloading("big.bin", 10, 100, 123L),
		)
		assertThat(downloading.extras.getString(Notification.EXTRA_TEXT))
			.isEqualTo("Connected to gbot at https://machine.ts.net")
	}

	private fun freeUnusedPort(): Int =
		java.net.ServerSocket(0).use { it.localPort }
}
