package com.gbot.android

import android.app.Activity
import android.content.Intent
import android.content.res.Configuration
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.FileProvider
import androidx.core.view.WindowInsetsControllerCompat
import androidx.fragment.app.Fragment
import java.io.File

class ChatFragment : Fragment() {

    companion object {
        // Persist across Fragment recreation — the system may destroy the
        // Fragment while the file picker / camera is open, and the recreated
        // Fragment needs the original callback and camera URI to deliver the
        // result to WebView.
        @Volatile
        private var pendingFileCallback: ValueCallback<Array<Uri>>? = null
        @Volatile
        private var cameraPhotoUri: Uri? = null
    }

    private var webView: WebView? = null
    private var loadAttempts = 0
    private val handler = Handler(Looper.getMainLooper())

    // registerForActivityResult must be called during Fragment initialization
    // (as a field initializer), NOT inside a method. This ensures the callback
    // survives Fragment recreation when the system kills it during file picker.
    private val filePickerLauncher: ActivityResultLauncher<Intent> =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            val callback = pendingFileCallback
            pendingFileCallback = null
            val data = result.data

            val results: Array<Uri>? = if (result.resultCode == Activity.RESULT_OK) {
                val uris = mutableListOf<Uri>()
                data?.data?.let { uris.add(it) }
                data?.clipData?.let { clip ->
                    for (i in 0 until clip.itemCount) {
                        uris.add(clip.getItemAt(i).uri)
                    }
                }
                if (uris.isNotEmpty()) uris.toTypedArray() else null
            } else {
                null
            }
            callback?.onReceiveValue(results)
        }

    private val cameraLauncher: ActivityResultLauncher<Intent> =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            val callback = pendingFileCallback
            pendingFileCallback = null
            val uri = cameraPhotoUri
            cameraPhotoUri = null
            // ACTION_IMAGE_CAPTURE without EXTRA_OUTPUT returns a thumbnail
            // Bitmap in data.extras, not a URI. We provide EXTRA_OUTPUT so the
            // camera writes a full-res JPEG to our FileProvider URI.
            callback?.onReceiveValue(
                if (result.resultCode == Activity.RESULT_OK && uri != null) arrayOf(uri) else null
            )
        }

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?
    ): View {
        val view = inflater.inflate(R.layout.fragment_chat, container, false)
        webView = view.findViewById(R.id.webView)
        return view
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        webView?.overScrollMode = View.OVER_SCROLL_NEVER
        webView?.settings?.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
        }
        // Web→native theme bridge: the WUI reports its effective theme so the
        // status-bar icon color matches the header background (WUI is the
        // source of truth — it may differ from the system theme). Must be
        // registered before the first loadUrl. Exposes exactly one primitive
        // method on trusted localhost-only content (minSdk 28, so the
        // pre-API-17 reflection hole does not apply).
        webView?.addJavascriptInterface(NativeThemeBridge(), "GBotNative")
        webView?.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(
                webView: WebView?,
                filePathCallback: ValueCallback<Array<Uri>>?,
                fileChooserParams: FileChooserParams?
            ): Boolean {
                pendingFileCallback?.onReceiveValue(null)
                pendingFileCallback = filePathCallback

                if (fileChooserParams?.isCaptureEnabled == true) {
                    // Create a temp file for the full-res photo and pass its
                    // URI via EXTRA_OUTPUT. Without this, the camera only
                    // returns a thumbnail Bitmap in data.extras — no URI.
                    val photoFile = File(context!!.cacheDir, "capture_${System.currentTimeMillis()}.jpg")
                    cameraPhotoUri = FileProvider.getUriForFile(
                        context!!, "${context!!.packageName}.fileprovider", photoFile
                    )
                    val intent = Intent(android.provider.MediaStore.ACTION_IMAGE_CAPTURE).apply {
                        putExtra(android.provider.MediaStore.EXTRA_OUTPUT, cameraPhotoUri)
                        addFlags(Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
                    }
                    try {
                        cameraLauncher.launch(intent)
                    } catch (e: Exception) {
                        pendingFileCallback = null
                        return false
                    }
                    return true
                }
                val intent = fileChooserParams?.createIntent() ?: return false
                val acceptTypes = fileChooserParams?.acceptTypes
                if (acceptTypes?.any { it.startsWith(".") } == true) {
                    intent.type = "*/*"
                }
                try {
                    filePickerLauncher.launch(intent)
                } catch (e: Exception) {
                    pendingFileCallback = null
                    return false
                }
                return true
            }
        }

        webView?.webViewClient = object : WebViewClient() {
            override fun onPageFinished(view: WebView?, url: String?) {
                // Android WebView's matchMedia('(prefers-color-scheme: light)')
                // initial value is unreliable — inject the real system theme
                // once the page is ready so resolveTheme('system') gets
                // corrected if it guessed wrong.
                applySystemTheme(view)
            }

            override fun onReceivedError(view: WebView?, request: WebResourceRequest?, error: WebResourceError?) {
                if (request?.isForMainFrame() == true) {
                    retryLoad()
                }
            }
        }

        tryLoad()
        updateStatusBarIcons()
    }

    private fun retryLoad() {
        if (loadAttempts < 10) {
            loadAttempts++
            handler.postDelayed({ tryLoad() }, 1000)
        } else {
            webView?.visibility = View.GONE
        }
    }

    private fun tryLoad() {
        webView?.visibility = View.VISIBLE
        webView?.loadUrl("http://localhost:8765/")
    }

    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        // Notify the web of the system flip; the WUI's GBotNative bridge
        // reports back the resolved theme and NativeThemeBridge updates the
        // status-bar icons. Do NOT set icons from the system theme here:
        // with an explicit user pref (dark/light) the system value is wrong.
        applySystemTheme(webView)
    }

    private fun applySystemTheme(view: WebView?) {
        val isLight = (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) ==
            Configuration.UI_MODE_NIGHT_NO
        view?.evaluateJavascript(
            "window.__gbotApplySystemTheme && window.__gbotApplySystemTheme($isLight);",
            null,
        )
    }

    private fun updateStatusBarIcons() {
        val isLight = (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) ==
            Configuration.UI_MODE_NIGHT_NO
        activity?.window?.let { window ->
            WindowInsetsControllerCompat(window, window.decorView).isAppearanceLightStatusBars = isLight
        }
    }

    /**
     * Exposed to the WUI as window.GBotNative. Called on WebView's
     * JavaBridge background thread — hop to main before touching the window.
     */
    private inner class NativeThemeBridge {
        @JavascriptInterface
        fun onThemeChanged(isDark: Boolean) {
            activity?.runOnUiThread {
                activity?.window?.let { window ->
                    WindowInsetsControllerCompat(window, window.decorView)
                        .isAppearanceLightStatusBars = !isDark
                }
            }
        }
    }

    override fun onDestroyView() {
        handler.removeCallbacksAndMessages(null)
        webView = null
        super.onDestroyView()
    }
}
