package com.gbot.android.server

import com.google.common.truth.Truth.assertThat
import org.junit.Test

class GbotWebSocketClientMimeTypeTest {

	@Test
	fun mimeTypeFor_mapsExtensionsCorrectly() {
		assertThat(GbotWebSocketClient.mimeTypeFor("app.apk")).isEqualTo("application/vnd.android.package-archive")
		assertThat(GbotWebSocketClient.mimeTypeFor("APP.APK")).isEqualTo("application/vnd.android.package-archive")
		assertThat(GbotWebSocketClient.mimeTypeFor("note.txt")).isEqualTo("text/plain")
		assertThat(GbotWebSocketClient.mimeTypeFor("photo.jpg")).isEqualTo("image/jpeg")
		assertThat(GbotWebSocketClient.mimeTypeFor("photo.jpeg")).isEqualTo("image/jpeg")
		assertThat(GbotWebSocketClient.mimeTypeFor("icon.png")).isEqualTo("image/png")
		assertThat(GbotWebSocketClient.mimeTypeFor("data.bin")).isEqualTo("application/octet-stream")
		assertThat(GbotWebSocketClient.mimeTypeFor("noext")).isEqualTo("application/octet-stream")
	}
}
