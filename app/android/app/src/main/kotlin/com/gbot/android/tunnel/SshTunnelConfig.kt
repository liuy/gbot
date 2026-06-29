package com.gbot.android.tunnel

import android.content.Context

data class SshTunnelConfig(
    val host: String = "",
    val port: Int = 22,
    val user: String = "",
    val password: String = "",
    val remotePort: Int = 8765,
    val localPort: Int = 8765
) {
    fun isValid(): Boolean = host.isNotBlank() && user.isNotBlank() && password.isNotBlank()

    companion object {
        private const val PREFS = "gbot_ssh"
        private const val K_HOST = "host"
        private const val K_PORT = "ssh_port"
        private const val K_USER = "user"
        private const val K_PASSWORD = "password"
        private const val K_REMOTE = "remote_port"
        private const val K_LOCAL = "local_port"

        fun load(ctx: Context): SshTunnelConfig {
            val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            return SshTunnelConfig(
                host = p.getString(K_HOST, "") ?: "",
                port = p.getInt(K_PORT, 22),
                user = p.getString(K_USER, "") ?: "",
                password = p.getString(K_PASSWORD, "") ?: "",
                remotePort = p.getInt(K_REMOTE, 8765),
                localPort = p.getInt(K_LOCAL, 8765)
            )
        }

        fun save(ctx: Context, cfg: SshTunnelConfig) {
            ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
                .putString(K_HOST, cfg.host)
                .putInt(K_PORT, cfg.port)
                .putString(K_USER, cfg.user)
                .putString(K_PASSWORD, cfg.password)
                .putInt(K_REMOTE, cfg.remotePort)
                .putInt(K_LOCAL, cfg.localPort)
                .apply()
        }
    }
}
