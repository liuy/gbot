package com.gbot.android

import android.content.Context
import android.util.Log
import android.system.Os
import java.io.File
import java.io.FileOutputStream
import java.util.zip.ZipInputStream

/**
 * Extracts the Termux bootstrap zip + gbot and rg binaries into filesDir/usr/.
 *
 * Bootstrap zip has flat structure (bin/, etc/, lib/ at root).
 * gbot and rg are separate assets copied into usr/bin/ after extraction.
 *
 * targetSdk 28 allows exec from filesDir — no jniLibs needed.
 */
object BootstrapInstaller {

    private const val TAG = "BootstrapInstaller"
    private const val VERSION_FILE = "bootstrap_version.txt"
    private const val BOOTSTRAP_VERSION = "11"

    fun ensureInstalled(context: Context, onLog: (String) -> Unit = {}, onError: (String) -> Unit = {}): File? {
        val prefixDir = File(context.filesDir, "usr")
        val versionFile = File(context.filesDir, VERSION_FILE)

        val alreadyInstalled = versionFile.exists() &&
            versionFile.readText().trim() == BOOTSTRAP_VERSION &&
            File(prefixDir, "bin/bash").exists()

        if (alreadyInstalled) {
            Log.i(TAG, "Bootstrap already installed")
            return File(prefixDir, "bin")
        }

        val stagingDir = File(context.filesDir, "usr-staging")
        val stagingPath = stagingDir.absolutePath

        Log.i(TAG, "Extracting bootstrap to $stagingPath...")
        try {
            if (stagingDir.exists()) stagingDir.deleteRecursively()
            stagingDir.mkdirs()

            // 1. Extract bootstrap zip (flat structure: bin/, etc/, lib/, var/, etc.)
            val symlinks = mutableListOf<Pair<String, String>>()
            val buffer = ByteArray(8096)

            context.assets.open("bootstrap-aarch64.zip").use { asset ->
                ZipInputStream(asset).use { zipInput ->
                    var zipEntry = zipInput.nextEntry
                    while (zipEntry != null) {
                        if (zipEntry.name == "SYMLINKS.txt") {
                            // Read symlinks — raw bytes, don't wrap in reader
                            val text = zipInput.readBytes().toString(Charsets.UTF_8)
                            text.lineSequence().forEach { line ->
                                val arrowIdx = line.indexOf('\u2190')
                                if (arrowIdx > 0) {
                                    val oldPath = line.substring(0, arrowIdx)
                                    val newPath = "$stagingPath/${line.substring(arrowIdx + 1)}"
                                    symlinks.add(oldPath to newPath)
                                    File(newPath).parentFile?.mkdirs()
                                }
                            }
                        } else {
                            val targetFile = File(stagingDir, zipEntry.name)
                            val canonicalPath = targetFile.canonicalPath
                            if (!canonicalPath.startsWith(stagingDir.canonicalPath + File.separator)) {
                                throw SecurityException("zip-slip detected: ${zipEntry.name}")
                            }
                            if (zipEntry.isDirectory) {
                                targetFile.mkdirs()
                            } else {
                                targetFile.parentFile?.mkdirs()
                                FileOutputStream(targetFile).use { out ->
                                    var n: Int
                                    while (true) {
                                        n = zipInput.read(buffer)
                                        if (n <= 0) break
                                        out.write(buffer, 0, n)
                                    }
                                }
                                // Termux sets all extracted files to 0700
                                Os.chmod(targetFile.absolutePath, 0x1C0)
                            }
                        }
                        zipInput.closeEntry()
                        zipEntry = zipInput.nextEntry
                    }
                }
            }

            // 2. Create symlinks
            var failedSymlinks = 0
            for ((target, linkPath) in symlinks) {
                try {
                    Os.symlink(target, linkPath)
                } catch (e: Exception) {
                    if (failedSymlinks < 5) {
                        Log.w(TAG, "symlink failed: $linkPath → $target: ${e.message}")
                        failedSymlinks++
                    }
                }
            }
            Log.i(TAG, "Created ${symlinks.size} symlinks")
            onLog("Extracted bootstrap + ${symlinks.size} symlinks")

            // 3. Move staging → prefix
            if (prefixDir.exists()) prefixDir.deleteRecursively()
            if (!stagingDir.renameTo(prefixDir)) {
                throw RuntimeException("rename staging → prefix failed")
            }

            // 4. Copy gbot binary into usr/bin/
            val binDir = File(prefixDir, "bin")
            val gbotBin = File(binDir, "gbot")
            context.assets.open("gbot-arm64").use { asset ->
                FileOutputStream(gbotBin).use { asset.copyTo(it) }
            }
            Os.chmod(gbotBin.absolutePath, 0x1C0) // 0700
            onLog("Injected gbot (${gbotBin.length()} bytes)")

            // 5. Copy rg binary into usr/bin/
            val rgBin = File(binDir, "rg")
            context.assets.open("rg-arm64").use { asset ->
                FileOutputStream(rgBin).use { asset.copyTo(it) }
            }
            Os.chmod(rgBin.absolutePath, 0x1C0) // 0700
            onLog("Injected rg (${rgBin.length()} bytes)")

            // second-stage writes into $TMPDIR, so it must exist before the
            // bootstrap script runs
            File(prefixDir, "tmp").mkdirs()

            // 6. Run second-stage bootstrap
            val secondStage = File(prefixDir, "etc/termux/termux-bootstrap/second-stage/termux-bootstrap-second-stage.sh")
            if (secondStage.exists()) {
                onLog("Running second-stage bootstrap...")
                val bashBin = File(prefixDir, "bin/bash")
                val pb2 = ProcessBuilder(bashBin.absolutePath, secondStage.absolutePath)
                    .redirectErrorStream(true)
                pb2.environment().apply {
                    put("HOME", context.filesDir.absolutePath)
                    put("PATH", "${prefixDir.absolutePath}/bin:/system/bin")
                    put("LD_LIBRARY_PATH", "${prefixDir.absolutePath}/lib")
                    put("TMPDIR", "${prefixDir.absolutePath}/tmp")
                    put("PREFIX", prefixDir.absolutePath)
                    put("TERMUX_PREFIX", prefixDir.absolutePath)
                    put("TERMUX_PACKAGE_MANAGER", "apt")
                    put("TERMUX_PACKAGE_ARCH", "aarch64")
                }
                val proc = pb2.start()
                val output = proc.inputStream.bufferedReader().readText()
                val exitCode = proc.waitFor()
                onLog("Second-stage exit=$exitCode")
                if (output.isNotBlank()) {
                    output.lines().takeLast(20).forEach { onLog(it) }
                }
                if (exitCode != 0) {
                    onLog("WARNING: second-stage failed")
                }
            }

            // 7. Finalize
            versionFile.writeText(BOOTSTRAP_VERSION)
            Log.i(TAG, "Bootstrap installed: ${prefixDir.absolutePath}")
            onLog("Bootstrap complete: ${prefixDir.absolutePath}")
            return binDir
        } catch (e: Exception) {
            val msg = "${e.javaClass.name}: ${e.message}"
            Log.e(TAG, "Bootstrap failed: $msg", e)
            onError(msg)
            return null
        }
    }
}
