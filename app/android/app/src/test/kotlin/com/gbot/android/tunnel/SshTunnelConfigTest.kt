package com.gbot.android.tunnel

import com.google.common.truth.Truth.assertThat
import org.junit.Test

class SshTunnelConfigTest {

	@Test
	fun isValid_allFieldsPresent_returnsTrue() {
		val cfg = SshTunnelConfig(
			host = "192.168.1.1",
			user = "root",
			password = "secret"
		)
		assertThat(cfg.isValid()).isTrue()
	}

	@Test
	fun isValid_blankHost_returnsFalse() {
		val cfg = SshTunnelConfig(host = "", user = "root", password = "secret")
		assertThat(cfg.isValid()).isFalse()
	}

	@Test
	fun isValid_whitespaceHost_returnsFalse() {
		val cfg = SshTunnelConfig(host = "   ", user = "root", password = "secret")
		assertThat(cfg.isValid()).isFalse()
	}

	@Test
	fun isValid_blankUser_returnsFalse() {
		val cfg = SshTunnelConfig(host = "1.2.3.4", user = "", password = "secret")
		assertThat(cfg.isValid()).isFalse()
	}

	@Test
	fun isValid_blankPassword_returnsFalse() {
		val cfg = SshTunnelConfig(host = "1.2.3.4", user = "root", password = "")
		assertThat(cfg.isValid()).isFalse()
	}

	@Test
	fun defaultValues_areCorrect() {
		val cfg = SshTunnelConfig()
		assertThat(cfg.host).isEmpty()
		assertThat(cfg.port).isEqualTo(22)
		assertThat(cfg.user).isEmpty()
		assertThat(cfg.password).isEmpty()
		assertThat(cfg.remotePort).isEqualTo(8765)
		assertThat(cfg.localPort).isEqualTo(8765)
	}

	@Test
	fun customPorts_arePreserved() {
		val cfg = SshTunnelConfig(
			host = "example.com",
			port = 2222,
			user = "admin",
			password = "pw",
			remotePort = 9999,
			localPort = 7777
		)
		assertThat(cfg.port).isEqualTo(2222)
		assertThat(cfg.remotePort).isEqualTo(9999)
		assertThat(cfg.localPort).isEqualTo(7777)
		assertThat(cfg.isValid()).isTrue()
	}
}
