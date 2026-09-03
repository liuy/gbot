package com.gbot.android

import android.os.Bundle
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import com.gbot.android.databinding.ActivityMainBinding
import com.google.android.material.bottomnavigation.BottomNavigationView

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        // Start gbot daemon locally (ProcessBuilder, not JNI). Runs in a
        // background thread to avoid blocking the UI during bootstrap extraction.
        Thread {
            GbotProcess.start(this) { msg ->
                android.util.Log.i("MainActivity", msg)
            }
        }.start()

        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        // Apply bottom system-bar inset as padding so the BottomNavigationView
        // is not obscured by the gesture pill / 3-button nav under edge-to-edge.
        // Do NOT use fitsSystemWindows on the root — it would consume the top
        // inset and break the transparent status bar blend.
        ViewCompat.setOnApplyWindowInsetsListener(binding.bottomNav) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            v.setPadding(0, 0, 0, bars.bottom)
            insets
        }

        binding.bottomNav.setOnItemSelectedListener { item ->
            when (item.itemId) {
                R.id.nav_chat -> {
                    swapFragment(ChatFragment())
                    true
                }
                R.id.nav_settings -> {
                    swapFragment(ControlFragment())
                    true
                }
                else -> false
            }
        }

        if (savedInstanceState == null) {
            // Idempotent initial selection: commit the fragment directly and
            // sync the nav state via the menu item (setChecked does NOT
            // dispatch the selection listener). Setting selectedItemId would
            // fire the listener a SECOND time (it already fired on listener
            // registration for the auto-selected first item), creating two
            // ChatFragments — the detached one's never-destroyed WebView
            // lived on as a zombie page that raced this one for the chat
            // slot (intermittent "taken_over" kicks on every app start).
            supportFragmentManager
                .beginTransaction()
                .replace(R.id.fragmentContainer, ChatFragment())
                .commit()
            binding.bottomNav.menu.findItem(R.id.nav_chat)?.isChecked = true
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        GbotProcess.stop()
    }

    private fun swapFragment(fragment: androidx.fragment.app.Fragment) {
        supportFragmentManager
            .beginTransaction()
            .replace(R.id.fragmentContainer, fragment)
            .commit()
    }
}
