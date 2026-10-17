package com.gbot.android

import android.app.Notification
import android.app.NotificationManager
import android.content.Intent
import android.os.Environment
import android.provider.MediaStore
import androidx.test.core.app.ApplicationProvider
import com.google.common.truth.Truth.assertThat
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.io.File
import java.util.concurrent.atomic.AtomicReference

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class ArtifactDownloaderTest {

	private lateinit var ctx: android.content.Context
	private lateinit var nm: NotificationManager
	private lateinit var media: FakeMediaProvider

	@Before
	fun setUp() {
		ctx = ApplicationProvider.getApplicationContext()
		GbotProcess.logBuffer.setLength(0)
		ArtifactDownloader.state.set(ArtifactDownloader.DownloadState.Idle)
		// Object statics outlive a test method (Robolectric only resets
		// android.* shadows) — a renderer left registered by another test
		// class's service would post into this test's fresh shadows.
		ArtifactDownloader.requestRender = null
		nm = ctx.getSystemService(NotificationManager::class.java)
		// The shadow's providers map is reset between methods: install per test.
		media = installFakeMediaStore(ctx)
	}

	@After
	fun tearDown() {
		// The worker thread outlives the test: a task left running would post
		// its notification into the next test's freshly-reset shadows.
		assertThat(ArtifactDownloader.awaitIdle()).isTrue()
	}

	@Test
	fun firstRun_createsItsOwnNotificationChannel() {
		assertThat(nm.getNotificationChannel(ArtifactDownloader.CHANNEL_ID)).isNull()

		val server = startServer(ByteArray(64))
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-ch.zip"), "dl-ch.zip", null, 0)

			assertThat(reason).isNull()
		} finally {
			server.stop()
		}

		val channel = nm.getNotificationChannel(ArtifactDownloader.CHANNEL_ID)
		assertThat(channel).isNotNull()
		assertThat(channel!!.id).isEqualTo("gbot_download_channel")
		assertThat(channel.importance).isEqualTo(NotificationManager.IMPORTANCE_DEFAULT)
	}

	@Test
	fun download_landsInDownloadsGbotWithTheExactBytes() {
		val body = ByteArray(4096) { (it % 251).toByte() }
		val server = startServer(body)
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-x.zip"), "dl-x.zip", "application/zip", 0)

			assertThat(reason).isNull()

			val inserts = insertStatementsFor(ctx, "dl-x.zip")
			assertThat(inserts).hasSize(1)
			assertThat(inserts[0].uri).isEqualTo(MediaStore.Downloads.EXTERNAL_CONTENT_URI)
			val values = inserts[0].contentValues
			assertThat(values.keySet()).containsExactly(
				MediaStore.MediaColumns.DISPLAY_NAME,
				MediaStore.MediaColumns.MIME_TYPE,
				MediaStore.MediaColumns.RELATIVE_PATH,
			)
			assertThat(values.getAsString(MediaStore.MediaColumns.DISPLAY_NAME)).isEqualTo("dl-x.zip")
			assertThat(values.getAsString(MediaStore.MediaColumns.MIME_TYPE)).isEqualTo("application/zip")
			assertThat(values.getAsString(MediaStore.MediaColumns.RELATIVE_PATH)).isEqualTo("Download/gbot")
			assertThat(media.streams.getValue(media.rows.first()).toByteArray()).isEqualTo(body)
			assertThat(GbotProcess.logBuffer.toString()).contains("dl-x.zip done (4096 bytes)")
		} finally {
			server.stop()
		}
	}

	@Test
	fun completionNotificationOpensTheFileWithItsMimeType() {
		val server = startServer(ByteArray(64))
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-app.apk"), "dl-app.apk", null, 0)

			assertThat(reason).isNull()
		} finally {
			server.stop()
		}

		val done = notificationsTitled("Download complete")
		assertThat(done).hasSize(1)
		val notification = done[0]
		assertThat(notification.channelId).isEqualTo(ArtifactDownloader.CHANNEL_ID)
		assertThat(notification.extras.getString(Notification.EXTRA_TEXT))
			.isEqualTo("Saved to Download/gbot/dl-app.apk")
		val pi = notification.contentIntent
		assertThat(pi).isNotNull()
		assertThat(shadowOf(pi).isImmutable()).isTrue()
		val intent = shadowOf(pi).savedIntent
		assertThat(intent.data).isEqualTo(media.rows.first())
		assertThat(intent.type).isEqualTo("application/vnd.android.package-archive")
		assertThat(intent.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION)
			.isNotEqualTo(0)
		assertThat(notificationsTitled("Download failed")).isEmpty()
	}

	@Test
	fun mediaStoreCollision_rendersTheRealStoredName() {
		// MediaStore renames on collision; the notification must report the
		// stored name, not the one we asked for.
		media.storedDisplayName = "dl-c (1).zip"
		val server = startServer(ByteArray(64))
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-c.zip"), "dl-c.zip", null, 0)
			assertThat(reason).isNull()
		} finally {
			server.stop()
		}
		val done = notificationsTitled("Download complete")
		assertThat(done).hasSize(1)
		assertThat(done[0].extras.getString(Notification.EXTRA_TEXT))
			.isEqualTo("Saved to Download/gbot/dl-c (1).zip")
	}

	@Test
	fun truncatedBody_deletesTheRowAndNotifiesTheReason() {
		val server = startServer(ByteArray(2048), truncateTo = 1024)
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-t.zip"), "dl-t.zip", null, 0)

			assertThat(reason).contains("1024")
			assertThat(reason).contains("2048")

			// Every attempt inserted a row, and every row was cleaned up in
			// mint order — the multi-attempt case the fake provider exists for.
			assertThat(insertStatementsFor(ctx, "dl-t.zip")).hasSize(3)
			assertThat(shadowOf(ctx.contentResolver).deletedUris)
				.containsExactlyElementsIn(media.rows)
				.inOrder()
			assertThat(media.streams.values.map { it.size() }).containsExactly(1024, 1024, 1024)

			val failed = notificationsTitled("Download failed")
			assertThat(failed).hasSize(1)
			val text = failed[0].extras.getString(Notification.EXTRA_TEXT)
			assertThat(text).contains("dl-t.zip")
			assertThat(text).contains("2048")
			assertThat(notificationsTitled("Download complete")).isEmpty()
		} finally {
			server.stop()
		}
	}

	@Test
	fun httpError_doesNotRetry() {
		val server = startServer(ByteArray(64), statusLine = "HTTP/1.1 404 Not Found")
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-404.zip"), "dl-404.zip", null, 0)

			assertThat(reason).contains("404")
			assertThat(server.accepts.get()).isEqualTo(1)
			// The status is checked before any row is opened: nothing to clean up.
			assertThat(insertStatementsFor(ctx, "dl-404.zip")).isEmpty()
			assertThat(notificationsTitled("Download failed")).hasSize(1)
		} finally {
			server.stop()
		}
	}

	@Test
	fun ioError_retriesThreeTimesThenFails() {
		val server = startAcceptAndCloseServer()
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-io.zip"), "dl-io.zip", null, 0)

			assertThat(reason).isNotEmpty()
			// The socket count is not the attempt count: the JDK's
			// HttpURLConnection retransmits a request itself when the server
			// closes before any response byte, so connections arrive in pairs.
			// The attempt count is pinned exactly by the event log below.
			assertThat(server.accepts.get()).isAtLeast(ArtifactDownloader.MAX_ATTEMPTS)
			// A connection-level fault dies inside openConnection, before
			// openSink — no row was ever opened, so none was deleted.
			assertThat(insertStatementsFor(ctx, "dl-io.zip")).isEmpty()
			assertThat(shadowOf(ctx.contentResolver).deletedUris).isEmpty()
			val failed = notificationsTitled("Download failed")
			assertThat(failed).hasSize(1)
			assertThat(failed[0].extras.getString(Notification.EXTRA_TEXT)).contains("dl-io.zip")
			// Exactly 3 attempts: retry lines for attempts 1 and 2 only, then
			// the final failure line (the third failure is not an "attempt N").
			val log = GbotProcess.logBuffer.toString()
			assertThat(log).contains("dl-io.zip attempt 1 failed")
			assertThat(log).contains("dl-io.zip attempt 2 failed")
			assertThat(log).doesNotContain("dl-io.zip attempt 3 failed")
			assertThat(log).contains("dl-io.zip failed: ")
		} finally {
			server.stop()
		}
	}

	@Test
	fun twoDownloadsEachGetTheirOwnCompletionNotification() {
		val bodyA = ByteArray(100) { (it % 251).toByte() }
		val bodyB = ByteArray(200) { (it % 241).toByte() }
		val serverA = startServer(bodyA)
		val serverB = startServer(bodyB)
		try {
			assertThat(
				ArtifactDownloader.run(ctx, serverA.url("dl-a.zip"), "dl-a.zip", null, 0)
			).isNull()
			assertThat(
				ArtifactDownloader.run(ctx, serverB.url("dl-b.zip"), "dl-b.zip", null, 0)
			).isNull()

			val done = notificationsTitled("Download complete")
			assertThat(done).hasSize(2)
			val requestCodes = done.map { shadowOf(it.contentIntent).requestCode }
			assertThat(requestCodes[0]).isNotEqualTo(requestCodes[1])
			assertThat(media.rows).hasSize(2)
			assertThat(media.streams.getValue(media.rows[0]).toByteArray()).isEqualTo(bodyA)
			assertThat(media.streams.getValue(media.rows[1]).toByteArray()).isEqualTo(bodyB)
		} finally {
			serverA.stop()
			serverB.stop()
		}
	}

	@Test
	fun unsupportedUrl_failsOnceWithAReason() {
		val reason = ArtifactDownloader.run(
			ctx, "blob:http://127.0.0.1:1/y", "dl-blob.zip", null, 0,
		)

		assertThat(reason).contains("unsupported url")
		assertThat(insertStatementsFor(ctx, "dl-blob.zip")).isEmpty()
		val failed = notificationsTitled("Download failed")
		assertThat(failed).hasSize(1)
		assertThat(failed[0].extras.getString(Notification.EXTRA_TEXT)).contains("unsupported url")
	}

	@Test
	fun mediaStoreOpenFailure_deletesTheRowAndFailsWithoutRetrying() {
		// The provider still mints and returns the URI and the shadow records
		// the insert, but registers no stream — openOutputStream misses the
		// map and the real resolver throws FileNotFoundException. Re-registration
		// under the same authority replaces setUp's provider, so `media` must
		// point at the active one.
		media = installFakeMediaStore(ctx, registerStreams = false)
		val server = startServer(ByteArray(256))
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-open.zip"), "dl-open.zip", null, 0)

			// The suffix is the platform-supplied message and differs by API
			// level — assert the production prefix only.
			assertThat(reason).startsWith("open output:")
			assertThat(server.accepts.get()).isEqualTo(1)
			assertThat(insertStatementsFor(ctx, "dl-open.zip")).hasSize(1)
			assertThat(shadowOf(ctx.contentResolver).deletedUris)
				.containsExactlyElementsIn(media.rows)
			assertThat(notificationsTitled("Download failed")).hasSize(1)
			assertThat(notificationsTitled("Download complete")).isEmpty()
		} finally {
			server.stop()
		}
	}

	@Test
	fun run_tracksTheSharedDownloadStateThroughItsLifecycle() {
		val midRun = AtomicReference<ArtifactDownloader.DownloadState?>(null)
		media.onInsert = { midRun.set(ArtifactDownloader.state.get()) }

		val server = startServer(ByteArray(64))
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-state.zip"), "dl-state.zip", null, 0)

			assertThat(reason).isNull()

			// Snapshot taken on the worker thread between openSink and pump.
			val snapshot = midRun.get()
			assertThat(snapshot).isInstanceOf(ArtifactDownloader.DownloadState.Downloading::class.java)
			snapshot as ArtifactDownloader.DownloadState.Downloading
			assertThat(snapshot.name).isEqualTo("dl-state.zip")
			assertThat(snapshot.startedAt).isGreaterThan(0)
			assertThat(ArtifactDownloader.state.get())
				.isEqualTo(ArtifactDownloader.DownloadState.Finished)
		} finally {
			server.stop()
		}
	}

	private fun notificationsTitled(title: String): List<Notification> =
		shadowOf(nm).allNotifications.filter {
			it.extras.getString(Notification.EXTRA_TITLE) == title
		}
}

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [28])
class ArtifactDownloaderApi28Test {

	private lateinit var ctx: android.content.Context
	private lateinit var nm: NotificationManager

	@Before
	fun setUp() {
		ctx = ApplicationProvider.getApplicationContext()
		GbotProcess.logBuffer.setLength(0)
		ArtifactDownloader.state.set(ArtifactDownloader.DownloadState.Idle)
		ArtifactDownloader.requestRender = null
		// Another sdk-28 test class may have left a stale FileProvider path
		// strategy (static cache, per-method temp roots) in this sandbox.
		resetFileProviderStrategyCache()
		nm = ctx.getSystemService(NotificationManager::class.java)
	}

	@After
	fun tearDown() {
		assertThat(ArtifactDownloader.awaitIdle()).isTrue()
	}

	@Test
	fun api28_writesToTheAppExternalDirAndNotifiesViaFileProvider() {
		val body = ByteArray(1024) { (it % 251).toByte() }
		val server = startServer(body)
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-28.zip"), "dl-28.zip", null, 0)

			assertThat(reason).isNull()

			val file = File(ctx.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS), "gbot/dl-28.zip")
			assertThat(file.exists()).isTrue()
			assertThat(file.readBytes()).isEqualTo(body)
			// An unguarded MediaStore call would record an insert statement
			// here (and trip lint NewApi) — the SDK_INT guard must hold.
			assertThat(shadowOf(ctx.contentResolver).insertStatements).isEmpty()

			val done = shadowOf(nm).allNotifications.filter {
				it.extras.getString(Notification.EXTRA_TITLE) == "Download complete"
			}
			assertThat(done).hasSize(1)
			val pi = done[0].contentIntent
			assertThat(pi).isNotNull()
			assertThat(shadowOf(pi).isImmutable()).isTrue()
			val data = shadowOf(pi).savedIntent.data
			assertThat(data).isNotNull()
			assertThat(data!!.scheme).isEqualTo("content")
			// The authority is ${applicationId}.fileprovider from the manifest —
			// derive it from the runtime package, never hardcode com.termux.
			assertThat(data.authority).isEqualTo(ctx.packageName + ".fileprovider")
			assertThat(GbotProcess.logBuffer.toString()).contains(file.absolutePath)
		} finally {
			server.stop()
		}
	}

	@Test
	fun api28_truncatedBodyLeavesNoPartialFile() {
		val server = startServer(ByteArray(512), truncateTo = 200)
		try {
			val reason = ArtifactDownloader.run(ctx, server.url("dl-28t.zip"), "dl-28t.zip", null, 0)

			assertThat(reason).isNotEmpty()
			val file = File(ctx.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS), "gbot/dl-28t.zip")
			assertThat(file.exists()).isFalse()
			assertThat(GbotProcess.logBuffer.toString()).contains("attempt 1 failed")
		} finally {
			server.stop()
		}
	}
}
