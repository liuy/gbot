package com.gbot.android

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.ActivityNotFoundException
import android.content.res.Configuration
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.view.animation.AlphaAnimation
import android.view.animation.Animation
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import androidx.browser.customtabs.CustomTabColorSchemeParams
import androidx.browser.customtabs.CustomTabsIntent
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import androidx.core.view.WindowInsetsControllerCompat
import androidx.fragment.app.Fragment
import java.io.File
import org.json.JSONObject

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

        // WUI target switch prefs — the single source of truth for pointing
        // the WebView at the LOCAL daemon or a user-configured REMOTE one.
        // The WUI reads/writes them only through the GBotNative bridge.
        private const val PREFS_FILE = "wui"
        const val KEY_TARGET = "wui_target" // "local" | "remote", default local
        const val KEY_REMOTE_NAME = "wui_remote_name" // display name, default ""
        const val KEY_REMOTE_HOST = "wui_remote_host" // default ""
        const val KEY_REMOTE_PORT = "wui_remote_port" // int, default 8765
        const val TARGET_LOCAL = "local"
        const val TARGET_REMOTE = "remote"
        private const val LOCAL_URL = "http://127.0.0.1:8765/"
        private const val DEFAULT_REMOTE_PORT = 8765

        // Companion-level so the URL math stays unit-testable without
        // Robolectric (its native binder does not load on arm64 Termux JVMs),
        // like ConnectionForegroundService.resolveTarget. An unparsable
        // remote config (blank host) degrades to the local daemon rather
        // than a malformed URL.
        internal fun buildTargetUrl(target: String, host: String, port: Int): String =
            if (target == TARGET_REMOTE && host.isNotBlank()) "http://$host:$port/" else LOCAL_URL
    }

    private var webView: WebView? = null
    // Last theme the WUI reported (GBotNative.onThemeChanged); dark is the
    // app's primary look so it doubles as the pre-report default.
    @Volatile private var isDarkTheme: Boolean = true
    private var loadingOverlay: View? = null
    private var splashMark: android.widget.TextView? = null
    // Escape hatch on the failure overlay: shown only when the REMOTE target
    // is unreachable, flips the pref back to local and reloads.
    private var backToLocal: TextView? = null
    private var lastLoadFailed = false
    private var loadAttempts = 0
    private val handler = Handler(Looper.getMainLooper())

    // Partial Custom Tabs REQUIRE launching via startActivityForResult (or a
    // CustomTabsSession) — a plain startActivity makes Chrome ignore the
    // height extra and open full-screen.
    private val customTabLauncher: ActivityResultLauncher<Intent> =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { }

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
        loadingOverlay = view.findViewById(R.id.loadingOverlay)
        splashMark = view.findViewById(R.id.splashMark)
        backToLocal = view.findViewById(R.id.backToLocal)
        startBreathing()
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
        // Read-only app-log bridge for the settings page's app-log panel
        // (same localhost-only trust scope as the theme bridge above).
        webView?.addJavascriptInterface(AppLogsBridge(), AppLogsBridge.BRIDGE_NAME)
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
            override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
                val url = request?.url ?: return false
                val scheme = url.scheme?.lowercase()
                if (scheme == "http" || scheme == "https") {
                    val host = url.host?.lowercase()
                    val port = if (url.port in 0..65535) url.port else if (scheme == "https") 443 else 80
                    // Own origin only (the WebView hosts JS bridges) — a chat
                    // link to any other localhost port must not stay in-app.
                    if ((host == "localhost" || host == "127.0.0.1" || host == "::1") && port == 8765) {
                        return false
                    }
                    // External http(s): partial bottom-sheet custom tab at
                    // half screen height (adjustable). Chrome builds without
                    // partial-tab support ignore the height extras and fall
                    // back to a full-screen tab.
                    openCustomTab(url)
                    return true
                }
                // mailto:, tel:, intent:, ... → hand to the system.
                return try {
                    startActivity(Intent(Intent.ACTION_VIEW, url))
                    true
                } catch (e: ActivityNotFoundException) {
                    true // nothing can open it — swallow so the WebView doesn't navigate
                }
            }

            override fun onPageFinished(view: WebView?, url: String?) {
                // onPageFinished ALSO fires for the system error page after
                // a failed load — only lift the splash on a real page.
                if (!lastLoadFailed) {
                    splashMark?.clearAnimation()
                    loadingOverlay?.visibility = View.GONE
                } else {
                    lastLoadFailed = false // consumed; next attempt starts clean
                }
                // Android WebView's matchMedia('(prefers-color-scheme: light)')
                // initial value is unreliable — inject the real system theme
                // once the page is ready so resolveTheme('system') gets
                // corrected if it guessed wrong.
                applySystemTheme(view)
                // Same injection pattern: push the current target + remote
                // name so the header wordmark renders the active target.
                applyTarget(view)
            }

            override fun onReceivedError(view: WebView?, request: WebResourceRequest?, error: WebResourceError?) {
                if (request?.isForMainFrame() == true) {
                    lastLoadFailed = true // the following onPageFinished is the error page's
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
            // Daemon never came up: wordless failure state — the wordmark
            // stops breathing, dims and flickers in the danger red; the
            // whole overlay becomes tap-to-retry.
            setFailureStyle()
            splashMark?.announceForAccessibility("守护进程启动失败，点按重试")
            loadingOverlay?.setOnClickListener {
                loadAttempts = 0
                backToLocal?.visibility = View.GONE
                startBreathing()
                tryLoad()
            }
            // The WUI header (and its target switch) is gone on an error
            // page — offer the escape hatch ONLY when the unreachable thing
            // is the user-configured remote; a failed local daemon has no
            // local to switch back to.
            if (currentTarget() == TARGET_REMOTE) {
                backToLocal?.visibility = View.VISIBLE
                backToLocal?.setOnClickListener {
                    targetPrefs().edit().putString(KEY_TARGET, TARGET_LOCAL).apply()
                    reloadToTarget()
                }
            } else {
                backToLocal?.visibility = View.GONE
            }
        }
    }

    private fun startBreathing() {
        splashMark?.let { mark ->
            mark.clearAnimation()
            mark.setTextColor(ContextCompat.getColor(requireContext(), R.color.splash_accent))
            mark.startAnimation(AlphaAnimation(0.35f, 1f).apply {
                duration = 1600
                repeatMode = Animation.REVERSE
                repeatCount = Animation.INFINITE
            })
        }
    }

    private fun setFailureStyle() {
        splashMark?.let { mark ->
            mark.clearAnimation()
            mark.setTextColor(ContextCompat.getColor(requireContext(), R.color.splash_danger))
            mark.startAnimation(AlphaAnimation(0.3f, 0.6f).apply {
                duration = 800
                repeatMode = Animation.REVERSE
                repeatCount = Animation.INFINITE
            })
        }
    }

    private fun tryLoad() {
        // Every attempt starts from a known state: without this reset, a
        // skipped/duplicated error-page onPageFinished could desync the
        // flag — worst case the splash sticks over a working page.
        lastLoadFailed = false
        loadingOverlay?.visibility = View.VISIBLE
        webView?.visibility = View.VISIBLE
        webView?.loadUrl(buildTargetUrl(currentTarget(), remoteHost(), remotePort()))
    }

    private fun targetPrefs() =
        requireContext().getSharedPreferences(PREFS_FILE, Context.MODE_PRIVATE)

    private fun currentTarget(): String =
        targetPrefs().getString(KEY_TARGET, TARGET_LOCAL) ?: TARGET_LOCAL

    private fun remoteName(): String =
        targetPrefs().getString(KEY_REMOTE_NAME, "") ?: ""

    private fun remoteHost(): String =
        targetPrefs().getString(KEY_REMOTE_HOST, "") ?: ""

    private fun remotePort(): Int =
        targetPrefs().getInt(KEY_REMOTE_PORT, DEFAULT_REMOTE_PORT)

    /**
     * Reload helper after a pref flip (bridge switchTarget, escape button):
     * back to the boot state, then load the (new) target's URL. onPageFinished
     * re-runs the theme + target injections on the fresh page.
     */
    private fun reloadToTarget() {
        handler.removeCallbacksAndMessages(null)
        loadAttempts = 0
        backToLocal?.visibility = View.GONE
        startBreathing()
        tryLoad()
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

    // JSONObject.quote: the remote NAME is user input — it must arrive as a
    // safely escaped JS string literal, never interpolated raw.
    private fun applyTarget(view: WebView?) {
        val target = JSONObject.quote(currentTarget())
        val name = JSONObject.quote(remoteName())
        view?.evaluateJavascript(
            "window.__gbotApplyTarget && window.__gbotApplyTarget($target, $name);",
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
                isDarkTheme = isDark
                activity?.window?.let { window ->
                    WindowInsetsControllerCompat(window, window.decorView)
                        .isAppearanceLightStatusBars = !isDark
                }
            }
        }

        // Fire-and-forget toggle from the header wordmark: flip wui_target
        // and reload the WebView to the new target's URL.
        @JavascriptInterface
        fun switchTarget() {
            activity?.runOnUiThread {
                val next = if (currentTarget() == TARGET_REMOTE) TARGET_LOCAL else TARGET_REMOTE
                // A remote flip without a configured host would load a junk
                // URL and strand the user on the retry overlay.
                if (next == TARGET_REMOTE && remoteHost().isBlank()) {
                    Toast.makeText(context, "远程未配置，请先在设置中填写", Toast.LENGTH_SHORT).show()
                    return@runOnUiThread
                }
                targetPrefs().edit().putString(KEY_TARGET, next).apply()
                reloadToTarget()
            }
        }

        // Persist the remote daemon config (host+port+name). Returns false
        // when validation failed (reason surfaced as a Kotlin toast) so the
        // page can skip its own saved-toast.
        @JavascriptInterface
        fun setRemoteTarget(name: String, host: String, port: Int): Boolean {
            val trimmedHost = host.trim()
            val hostOk = trimmedHost.isNotEmpty() && trimmedHost.none { it.isWhitespace() } &&
                '/' !in trimmedHost && ':' !in trimmedHost
            val portOk = port in 1..65535
            activity?.runOnUiThread {
                val message = when {
                    !hostOk -> "远程主机格式无效（仅主机名，端口单独填写）"
                    !portOk -> "端口必须是 1-65535"
                    else -> return@runOnUiThread
                }
                Toast.makeText(context, message, Toast.LENGTH_SHORT).show()
            }
            if (hostOk && portOk) {
                targetPrefs().edit()
                    .putString(KEY_REMOTE_NAME, name.trim())
                    .putString(KEY_REMOTE_HOST, trimmedHost)
                    .putInt(KEY_REMOTE_PORT, port)
                    .apply()
            }
            return hostOk && portOk
        }

        // JSON config for the settings REMOTE card's prefill. Reads prefs
        // directly on the bridge thread — SharedPreferences is thread-safe
        // and this must not depend on the fragment being attached.
        @JavascriptInterface
        fun getRemoteTarget(): String {
            val prefs = context?.getSharedPreferences(PREFS_FILE, Context.MODE_PRIVATE)
            val json = JSONObject()
            json.put("target", prefs?.getString(KEY_TARGET, TARGET_LOCAL) ?: TARGET_LOCAL)
            json.put("name", prefs?.getString(KEY_REMOTE_NAME, "") ?: "")
            json.put("host", prefs?.getString(KEY_REMOTE_HOST, "") ?: "")
            json.put("port", prefs?.getInt(KEY_REMOTE_PORT, DEFAULT_REMOTE_PORT) ?: DEFAULT_REMOTE_PORT)
            return json.toString()
        }
    }

    private fun openCustomTab(url: Uri) {
        // Follow the WUI-resolved theme (web is the single source of truth);
        // dark matches the splash/WUI palette byte-for-byte via splash_bg.
        val scheme = if (isDarkTheme) CustomTabsIntent.COLOR_SCHEME_DARK else CustomTabsIntent.COLOR_SCHEME_LIGHT
        val params = CustomTabColorSchemeParams.Builder()
            .setToolbarColor(
                ContextCompat.getColor(
                    requireContext(),
                    if (isDarkTheme) R.color.splash_bg else android.R.color.white,
                ),
            )
            .build()
        val intent = CustomTabsIntent.Builder()
            .setInitialActivityHeightPx(
                resources.displayMetrics.heightPixels / 2,
                CustomTabsIntent.ACTIVITY_HEIGHT_ADJUSTABLE,
            )
            .setToolbarCornerRadiusDp(16)
            .setDefaultColorSchemeParams(params)
            .setColorScheme(scheme)
            // setData + startActivityForResult-style launch: the partial-tab
            // contract. launchUrl() (plain startActivity) makes Chrome ignore
            // the height and open full-screen.
            .build()
        intent.intent.data = url
        customTabLauncher.launch(intent.intent)
    }

    override fun onDestroyView() {
        handler.removeCallbacksAndMessages(null)
        // A detached-but-alive WebView keeps its page (and WebSockets)
        // running — the source of phantom chat-slot clients. Destroy it.
        webView?.destroy()
        webView = null
        loadingOverlay = null
        splashMark = null
        backToLocal = null
        super.onDestroyView()
    }
}
