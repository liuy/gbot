package com.gbot.android

import android.content.Intent
import android.net.Uri
import java.io.File
import java.util.Base64

/** window.GBotShare — WUI long-press message share bridge (ChatFragment inner
 *  class registers it). Kept as a const so JS and Kotlin can never drift. */
const val SHARE_BRIDGE_NAME = "GBotShare"

private const val SHARE_DATA_PREFIX = "data:image/png;base64,"

/** Strict parse of "data:image/png;base64,<payload>". Wrong mime, missing
 *  prefix, invalid base64 or an empty result all return null.
 *  java.util.Base64 (API 26+, minSdk 28 is fine) and — unlike
 *  android.util.Base64 — it is a real implementation under plain-JVM unit
 *  tests, where the android.jar stubs would blow up. */
internal fun decodeShareDataUrl(dataUrl: String): ByteArray? {
    if (!dataUrl.startsWith(SHARE_DATA_PREFIX)) return null
    val bytes = try {
        Base64.getDecoder().decode(dataUrl.substring(SHARE_DATA_PREFIX.length))
    } catch (e: IllegalArgumentException) {
        return null
    }
    return bytes.takeIf { it.isNotEmpty() }
}

private const val SHARE_FILE_FALLBACK = "gbot-share.png"

/** Keep [A-Za-z0-9._-], replace everything else with '_'; an empty result or
 *  a bare "." / ".." (both survive cleaning untouched because every character
 *  is whitelisted) falls back to the default name. Separators never survive,
 *  so path traversal is impossible by construction; over 120 chars truncate.
 */
internal fun sanitizeShareFileName(name: String): String {
    val cleaned = name.replace(Regex("[^A-Za-z0-9._-]"), "_")
    if (cleaned.isEmpty() || cleaned == "." || cleaned == "..") return SHARE_FILE_FALLBACK
    return cleaned.take(120)
}

const val SHARE_TEXT_MIME = "text/markdown"

/** Write the raw markdown source into cacheDir/share (same home and name
 *  sanitizing as the PNG path) and hand the file back for FileProvider.
 *  No empty-content guard: the WUI gates on non-empty trimmed text. */
internal fun writeShareText(cacheDir: File, content: String, fileName: String): File {
    val dir = File(cacheDir, "share").apply { mkdirs() }
    val file = File(dir, sanitizeShareFileName(fileName))
    file.writeText(content)
    return file
}

/** File-only ACTION_SEND: some targets (WeChat) may not surface for
 *  text/markdown — a known limitation already communicated to the user. */
internal fun buildShareTextSendIntent(uri: Uri): Intent =
    Intent(Intent.ACTION_SEND).apply {
        type = SHARE_TEXT_MIME
        putExtra(Intent.EXTRA_STREAM, uri)
        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
