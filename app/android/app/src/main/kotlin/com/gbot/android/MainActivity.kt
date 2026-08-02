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
            binding.bottomNav.selectedItemId = R.id.nav_chat
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
