package com.gbot.android

import android.content.Intent
import android.os.Bundle
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import com.gbot.android.databinding.ActivityMainBinding
import com.gbot.android.service.ConnectionForegroundService

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        // Start gbot daemon locally (ProcessBuilder, not JNI). Runs in a
        // background thread to avoid blocking the UI during bootstrap extraction.
        GbotProcess.lastExitInfo = GbotProcess.readLastExitReason(this)
        Thread {
            GbotProcess.start(this) { msg ->
                android.util.Log.i("MainActivity", msg)
            }
        }.start()

        // Foreground importance: the OS must neither freeze the app's cgroup
        // (cached-apps freezer stops the daemon) nor reap the daemon as a
        // phantom child process once the UI goes to the background.
        ContextCompat.startForegroundService(
            this,
            Intent(this, ConnectionForegroundService::class.java)
                .putExtra(ConnectionForegroundService.EXTRA_HOST, ConnectionForegroundService.DEFAULT_HOST)
                .putExtra(ConnectionForegroundService.EXTRA_PORT, ConnectionForegroundService.DEFAULT_PORT),
        )

        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        // Apply bottom system-bar inset as padding so the WebView is not
        // obscured by the gesture pill / 3-button nav under edge-to-edge.
        // Do NOT use fitsSystemWindows on the root — it would consume the top
        // inset and break the transparent status bar blend.
        ViewCompat.setOnApplyWindowInsetsListener(binding.fragmentContainer) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            v.setPadding(0, 0, 0, bars.bottom)
            insets
        }

        if (savedInstanceState == null) {
            // ChatFragment is the single full-screen body — no nav shell, so
            // there is no selection state to sync, just one commit.
            supportFragmentManager
                .beginTransaction()
                .replace(R.id.fragmentContainer, ChatFragment())
                .commit()
        }
    }

    override fun onResume() {
        super.onResume()
        // Re-arm the runit supervisor on EVERY foreground return, not just
        // onCreate: swiping the recents card usually keeps the App PROCESS
        // alive, so a reopen resumes without ever calling onCreate — and a
        // supervisor killed meanwhile (phantom reaper, daemon restarts)
        // would stay dead until the next cold start.
        Thread {
            GbotProcess.ensureTermuxServices(applicationContext) { msg ->
                android.util.Log.i("MainActivity", msg)
            }
        }.start()
    }

    override fun onDestroy() {
        super.onDestroy()
        GbotProcess.stop()
    }
}
