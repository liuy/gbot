package com.gbot.android

enum class StatusState(val textResId: Int, val colorResId: Int) {
    CONNECTED(R.string.status_connected, R.color.status_green),
    WAITING(R.string.status_waiting, R.color.status_orange),
    DISCONNECTED(R.string.status_disconnected, R.color.status_red)
}
