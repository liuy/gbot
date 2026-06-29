package com.gbot.android.tunnel

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import com.google.common.truth.Truth.assertThat
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33])
class SshTunnelConfigPersistenceTest {

	private val ctx: Context = ApplicationProvider.getApplicationContext()

	@Test
	fun load_emptyPrefs_returnsDefaults() {
		val cfg = SshTunnelConfig.load(ctx)
		assertThat(cfg.host).isEmpty()
		assertThat(cfg.port).isEqualTo(22)
		assertThat(cfg.user).isEmpty()
		assertThat(cfg.password).isEmpty()
		assertThat(cfg.remotePort).isEqualTo(8765)
		assertThat(cfg.localPort).isEqualTo(8765)
	}

	@Test
	fun save_thenLoad_roundTripsAllFields() {
		val saved = SshTunnelConfig(
			host = "host.example",
			port = 2222,
			user = "admin",
			password = "pw",
			remotePort = 9999,
			localPort = 7777
		)
		SshTunnelConfig.save(ctx, saved)

		val loaded = SshTunnelConfig.load(ctx)
		assertThat(loaded.host).isEqualTo("host.example")
		assertThat(loaded.port).isEqualTo(2222)
		assertThat(loaded.user).isEqualTo("admin")
		assertThat(loaded.password).isEqualTo("pw")
		assertThat(loaded.remotePort).isEqualTo(9999)
		assertThat(loaded.localPort).isEqualTo(7777)
	}

	@Test
	fun save_overwritesPreviousValues() {
		SshTunnelConfig.save(ctx, SshTunnelConfig(host = "first.example", user = "u1", password = "p1"))
		SshTunnelConfig.save(
			ctx,
			SshTunnelConfig(host = "second.example", port = 2222, user = "u2", password = "p2", remotePort = 9999, localPort = 7777)
		)

		val loaded = SshTunnelConfig.load(ctx)
		assertThat(loaded.host).isEqualTo("second.example")
		assertThat(loaded.user).isEqualTo("u2")
		assertThat(loaded.password).isEqualTo("p2")
		assertThat(loaded.port).isEqualTo(2222)
		assertThat(loaded.remotePort).isEqualTo(9999)
		assertThat(loaded.localPort).isEqualTo(7777)
	}

	@Test
	fun load_afterPartialSaveViaRawPrefs_reflectsExternalWrites() {
		ctx.getSharedPreferences("gbot_ssh", Context.MODE_PRIVATE)
			.edit()
			.putString("host", "x")
			.commit()

		val loaded = SshTunnelConfig.load(ctx)
		assertThat(loaded.host).isEqualTo("x")
		assertThat(loaded.port).isEqualTo(22)
		assertThat(loaded.user).isEmpty()
		assertThat(loaded.password).isEmpty()
		assertThat(loaded.remotePort).isEqualTo(8765)
		assertThat(loaded.localPort).isEqualTo(8765)
	}
}
