package com.gbot.android.service

import com.google.common.truth.Truth.assertThat
import org.junit.Test

// Plain JVM test — no Robolectric (same reason as resolveTarget's test):
// pins the URI math connectLoop dials for the three host forms.
class ConnectionForegroundServiceEventsUriTest {

    @Test
    fun httpsOrigin_mapsToWss_andIgnoresThePortExtra() {
        assertThat(ConnectionForegroundService.buildEventsUri("https://machine.ts.net", 8765))
            .isEqualTo(java.net.URI("wss://machine.ts.net/ws"))
    }

    @Test
    fun httpOrigin_mapsToWsOnTheSameAuthority() {
        assertThat(ConnectionForegroundService.buildEventsUri("http://nas.box:9000", 8765))
            .isEqualTo(java.net.URI("ws://nas.box:9000/ws"))
    }

    @Test
    fun bareHost_keepsTheLegacyHostPortForm() {
        assertThat(ConnectionForegroundService.buildEventsUri("192.168.1.5", 9000))
            .isEqualTo(java.net.URI("ws://192.168.1.5:9000/ws"))
    }

    @Test
    fun originTrailingSlashes_areTrimmedBeforeThePath() {
        assertThat(ConnectionForegroundService.buildEventsUri("https://machine.ts.net/", 8765))
            .isEqualTo(java.net.URI("wss://machine.ts.net/ws"))
        assertThat(ConnectionForegroundService.buildEventsUri("http://nas.box//", 8765))
            .isEqualTo(java.net.URI("ws://nas.box/ws"))
    }

    @Test
    fun unparsableUri_yieldsNullInsteadOfThrowing() {
        // A malformed origin (unterminated IPv6 bracket) must degrade to
        // null so connectLoop can log-and-backoff instead of crash-looping.
        assertThat(ConnectionForegroundService.buildEventsUri("https://[bad", 8765)).isNull()
    }

    @Test
    fun targetLabel_originHostShowsVerbatim_bareHostAppendsThePort() {
        assertThat(ConnectionForegroundService.targetLabel("https://machine.ts.net", 8765))
            .isEqualTo("https://machine.ts.net")
        assertThat(ConnectionForegroundService.targetLabel("192.168.1.5", 9000))
            .isEqualTo("192.168.1.5:9000")
    }
}
