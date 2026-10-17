package com.gbot.android

import com.google.common.truth.Truth.assertThat
import org.junit.Test
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.net.HttpURLConnection

/**
 * The context-free HTTP core (openConnection + pump) against a real loopback
 * server — plain JUnit, no Robolectric: the byte-level behaviour must hold
 * with the exact JDK classes the production code calls, unshadowed.
 */
class ArtifactDownloaderHttpTest {

	@Test
	fun pump_writesExactlyTheServedBodyAndReportsItsLength() {
		val body = ByteArray(4096) { (it % 251).toByte() }
		val server = startServer(body)
		try {
			val conn = ArtifactDownloader.openConnection(server.url("artifacts/x.zip"))
			val out = ByteArrayOutputStream()
			val written = ArtifactDownloader.pump(conn, out)

			assertThat(written).isEqualTo(4096L)
			assertThat(out.toByteArray()).isEqualTo(body)
		} finally {
			server.stop()
		}
	}

	@Test
	fun openConnection_sendsIdentityEncodingAndGetLine() {
		val server = startServer(ByteArray(16))
		try {
			ArtifactDownloader.openConnection(server.url("artifacts/big.glb"))

			val request = server.requests.first()
			// Server-side capture: the header can only be pinned by what the
			// server received, never inferred from behaviour (the JDK never
			// adds Accept-Encoding on its own, so a missing header is silent).
			assertThat(request).contains("GET /artifacts/big.glb HTTP/1.1")
			assertThat(request).contains("Accept-Encoding: identity")
		} finally {
			server.stop()
		}
	}

	@Test
	fun openConnection_configuresBoundedTimeouts() {
		val server = startServer(ByteArray(16))
		try {
			val conn = ArtifactDownloader.openConnection(server.url("artifacts/x.zip"))

			assertThat(conn.connectTimeout).isEqualTo(ArtifactDownloader.CONNECT_TIMEOUT_MS)
			assertThat(conn.readTimeout).isEqualTo(ArtifactDownloader.READ_TIMEOUT_MS)
		} finally {
			server.stop()
		}
	}

	@Test
	fun pump_detectsATruncatedBodyAsAFailure() {
		val server = startServer(ByteArray(2048), truncateTo = 1024)
		try {
			val conn = ArtifactDownloader.openConnection(server.url("artifacts/x.zip"))

			val error = runCatching {
				ArtifactDownloader.pump(conn, ByteArrayOutputStream())
			}.exceptionOrNull()

			assertThat(error).isInstanceOf(IOException::class.java)
			assertThat(error!!.message).contains("1024")
			assertThat(error.message).contains("2048")
		} finally {
			server.stop()
		}
	}

	@Test
	fun pump_acceptsABodyOfUnknownLength() {
		val server = startServer(
			ByteArray(300),
			extraHeaders = listOf("Connection: close"),
			omitContentLength = true,
		)
		try {
			val conn = ArtifactDownloader.openConnection(server.url("artifacts/x.zip"))
			val out = ByteArrayOutputStream()

			val written = ArtifactDownloader.pump(conn, out)

			assertThat(written).isEqualTo(300L)
			assertThat(out.size()).isEqualTo(300)
		} finally {
			server.stop()
		}
	}

	@Test
	fun openConnection_reportsTheHttpStatusAsTheReason() {
		val server = startServer(ByteArray(16), statusLine = "HTTP/1.1 404 Not Found")
		try {
			val error = runCatching {
				ArtifactDownloader.openConnection(server.url("artifacts/missing.zip"))
			}.exceptionOrNull()

			assertThat(error).isInstanceOf(ArtifactDownloader.HttpFailure::class.java)
			assertThat(error!!.message).contains("404")
			assertThat(server.accepts.get()).isEqualTo(1)
		} finally {
			server.stop()
		}
	}

	@Test
	fun openConnection_mapsABlobUrlToANonRetryableReason() {
		val error = runCatching {
			ArtifactDownloader.openConnection("blob:http://127.0.0.1:1/y")
		}.exceptionOrNull()

		assertThat(error).isInstanceOf(ArtifactDownloader.HttpFailure::class.java)
		assertThat(error!!.message).contains("unsupported url")
	}
}
