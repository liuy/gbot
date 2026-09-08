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
import com.google.gson.Gson
import com.google.gson.JsonArray
import com.google.gson.JsonObject
import com.google.gson.JsonParser
import com.google.gson.JsonPrimitive
import java.io.File

class ChatFragment : Fragment() {

    /** One user-configured remote daemon endpoint. NAME is the identity. */
    data class RemoteTarget(val name: String, val host: String, val port: Int)

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
        // the WebView at the LOCAL daemon or one of several user-configured
        // REMOTE ones. The WUI reads/writes them only through the GBotNative
        // bridge. KEY_REMOTES holds a JSON array of named endpoints (name is
        // the identity); KEY_CURRENT names the active endpoint when the
        // target is remote.
        private const val PREFS_FILE = "wui"
        const val KEY_TARGET = "wui_target" // "local" | "remote", default local
        const val KEY_REMOTES = "wui_remotes" // JSON array [{"name","host","port"}, ...]
        const val KEY_CURRENT = "wui_current" // remote NAME when target == remote
        // Legacy single-remote keys — consumed once by migrateLegacyRemote,
        // then removed. No code writes them anymore.
        private const val KEY_REMOTE_NAME = "wui_remote_name"
        private const val KEY_REMOTE_HOST = "wui_remote_host"
        private const val KEY_REMOTE_PORT = "wui_remote_port"
        const val TARGET_LOCAL = "local"
        const val TARGET_REMOTE = "remote"
        private const val LOCAL_URL = "http://127.0.0.1:8765/"
        private const val DEFAULT_REMOTE_PORT = 8765

        // Rejection reasons surfaced as Kotlin toasts (the page pre-validates
        // with its own i18n'd copies; these are the backstop).
        internal const val MSG_NAME_REQUIRED = "名称不能为空"
        internal const val MSG_NAME_DUPLICATE = "名称不能重复"
        internal const val MSG_HOST_INVALID = "远程主机格式无效（仅主机名，端口单独填写）"
        internal const val MSG_PORT_INVALID = "端口必须是 1-65535"

        // Companion-level so the endpoint-list math stays unit-testable
        // without Robolectric (its native binder does not load on arm64
        // Termux JVMs), like buildTargetUrl above. Gson does the JSON work —
        // org.json is stubbed (throws) in local unit tests.

        /**
         * Parse the stored/passed JSON array of {"name","host","port"}.
         * ANY malformation (non-JSON, non-array, non-object element) degrades
         * to an empty list — never throws. Missing fields parse to values
         * that fail validation ("", 0).
         */
        internal fun parseRemoteTargets(json: String?): List<RemoteTarget> {
            if (json.isNullOrBlank()) return emptyList()
            return try {
                val element = JsonParser.parseString(json)
                if (!element.isJsonArray) return emptyList()
                val targets = mutableListOf<RemoteTarget>()
                for (el in element.asJsonArray) {
                    if (!el.isJsonObject) return emptyList()
                    val obj = el.asJsonObject
                    val name = (obj.get("name") as? JsonPrimitive)?.asString ?: ""
                    val host = (obj.get("host") as? JsonPrimitive)?.asString ?: ""
                    val port = try {
                        (obj.get("port") as? JsonPrimitive)?.asInt ?: 0
                    } catch (e: NumberFormatException) {
                        0
                    }
                    targets.add(RemoteTarget(name, host, port))
                }
                targets
            } catch (e: Exception) {
                emptyList()
            }
        }

        /** Serialize entries to the canonical stored JSON array shape. */
        internal fun serializeRemoteTargets(targets: List<RemoteTarget>): String {
            val array = JsonArray()
            for (t in targets) {
                val obj = JsonObject()
                obj.addProperty("name", t.name)
                obj.addProperty("host", t.host)
                obj.addProperty("port", t.port)
                array.add(obj)
            }
            return array.toString()
        }

        /**
         * Validate EVERY entry (after edge-trimming): name non-empty and
         * unique, host non-blank with no whitespace, '/' or ':', port
         * 1-65535. Returns null when the whole list is valid, else the toast
         * message for the first offending entry. An empty list is valid —
         * clearing all remotes is a legitimate save.
         */
        internal fun validateRemoteTargets(targets: List<RemoteTarget>): String? {
            val seen = HashSet<String>()
            for (t in targets) {
                val name = t.name.trim()
                val host = t.host.trim()
                if (name.isEmpty()) return MSG_NAME_REQUIRED
                if (host.isEmpty() || host.any { it.isWhitespace() } ||
                    '/' in host || ':' in host
                ) return MSG_HOST_INVALID
                if (t.port !in 1..65535) return MSG_PORT_INVALID
                if (!seen.add(name)) return MSG_NAME_DUPLICATE
            }
            return null
        }

        /**
         * Legacy single-entry migration: the old prefs held one optional
         * name + host + port. A blank host means nothing was configured →
         * null; a blank display name falls back to the host so the entry
         * still satisfies the "name non-empty" identity rule.
         */
        internal fun legacyRemoteTarget(name: String, host: String, port: Int): RemoteTarget? {
            val trimmedHost = host.trim()
            if (trimmedHost.isEmpty()) return null
            return RemoteTarget(name.trim().ifEmpty { trimmedHost }, trimmedHost, port)
        }

        /**
         * Post-save reconciliation of the target pref: a save that removed
         * or renamed the endpoint named by wui_current would strand the next
         * launch on a name that matches nothing (currentRemote() → null →
         * silently local URL). Returns TARGET_LOCAL when the active remote
         * no longer exists, null when the prefs need no change.
         */
        internal fun reconcileTargetAfterSave(
            target: String,
            current: String,
            saved: List<RemoteTarget>,
        ): String? =
            if (target == TARGET_REMOTE && saved.none { it.name == current }) TARGET_LOCAL else null

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
        migrateLegacyRemote()
        // Every attempt starts from a known state: without this reset, a
        // skipped/duplicated error-page onPageFinished could desync the
        // flag — worst case the splash sticks over a working page.
        lastLoadFailed = false
        loadingOverlay?.visibility = View.VISIBLE
        webView?.visibility = View.VISIBLE
        val remote = currentRemote()
        webView?.loadUrl(
            buildTargetUrl(currentTarget(), remote?.host ?: "", remote?.port ?: DEFAULT_REMOTE_PORT)
        )
    }

    private fun targetPrefs() =
        requireContext().getSharedPreferences(PREFS_FILE, Context.MODE_PRIVATE)

    private fun currentTarget(): String =
        targetPrefs().getString(KEY_TARGET, TARGET_LOCAL) ?: TARGET_LOCAL

    private fun remoteTargetsList(): List<RemoteTarget> =
        parseRemoteTargets(targetPrefs().getString(KEY_REMOTES, null))

    private fun currentRemoteName(): String =
        targetPrefs().getString(KEY_CURRENT, "") ?: ""

    /** The active endpoint when target == remote, matched by NAME. */
    private fun currentRemote(): RemoteTarget? =
        if (currentTarget() == TARGET_REMOTE) {
            remoteTargetsList().firstOrNull { it.name == currentRemoteName() }
        } else {
            null
        }

    /**
     * One-time upgrade from the single-endpoint prefs: a configured legacy
     * host becomes the first array entry (unmigrated lists only); the old
     * keys are always removed so the migration never re-runs. Runs from
     * tryLoad, before any page or bridge can observe the prefs.
     */
    private fun migrateLegacyRemote() {
        val prefs = targetPrefs()
        if (!prefs.contains(KEY_REMOTE_NAME) && !prefs.contains(KEY_REMOTE_HOST) &&
            !prefs.contains(KEY_REMOTE_PORT)
        ) {
            return
        }
        if (remoteTargetsList().isEmpty()) {
            legacyRemoteTarget(
                prefs.getString(KEY_REMOTE_NAME, "") ?: "",
                prefs.getString(KEY_REMOTE_HOST, "") ?: "",
                prefs.getInt(KEY_REMOTE_PORT, DEFAULT_REMOTE_PORT),
            )?.let {
                // Seed wui_current too: a legacy target=remote must keep
                // pointing at the migrated entry, whose lookup is by NAME.
                prefs.edit()
                    .putString(KEY_REMOTES, serializeRemoteTargets(listOf(it)))
                    .putString(KEY_CURRENT, it.name)
                    .apply()
            }
        }
        prefs.edit()
            .remove(KEY_REMOTE_NAME)
            .remove(KEY_REMOTE_HOST)
            .remove(KEY_REMOTE_PORT)
            .apply()
    }

    /**
     * Reload helper after a pref flip (bridge switchTo, escape button):
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

    // The payload is ONE JSON object {target, current, remotes} delivered as
    // a quoted JS string literal — Gson's string serialization is JSON-strict
    // (JSON strings are valid JS string literals, and Gson escapes U+2028/
    // 2029 too), so the user-controlled remote NAME can never break out of
    // the literal.
    private fun applyTarget(view: WebView?) {
        val payload = JsonObject()
        payload.addProperty("target", currentTarget())
        payload.addProperty("current", currentRemoteName())
        val remotes = JsonArray()
        for (t in remoteTargetsList()) {
            val obj = JsonObject()
            obj.addProperty("name", t.name)
            obj.addProperty("host", t.host)
            obj.addProperty("port", t.port)
            remotes.add(obj)
        }
        payload.add("remotes", remotes)
        view?.evaluateJavascript(
            "window.__gbotApplyTarget && window.__gbotApplyTarget(${Gson().toJson(payload.toString())});",
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

        // JSON array of configured remote endpoints for the settings REMOTE
        // card's prefill. Reads prefs directly on the bridge thread —
        // SharedPreferences is thread-safe and this must not depend on the
        // fragment being attached.
        @JavascriptInterface
        fun getRemoteTargets(): String =
            context?.getSharedPreferences(PREFS_FILE, Context.MODE_PRIVATE)
                ?.getString(KEY_REMOTES, null)?.takeIf { it.isNotBlank() } ?: "[]"

        // Persist the WHOLE endpoint list (settings card saves all entries in
        // one shot). Returns false when validation failed (reason surfaced as
        // a Kotlin toast) so the page can skip its own saved-toast.
        @JavascriptInterface
        fun setRemoteTargets(json: String): Boolean {
            if (json.isBlank()) {
                activity?.runOnUiThread {
                    Toast.makeText(context, MSG_HOST_INVALID, Toast.LENGTH_SHORT).show()
                }
                return false
            }
            val targets = parseRemoteTargets(json)
            val error = validateRemoteTargets(targets)
            if (error != null) {
                activity?.runOnUiThread {
                    Toast.makeText(context, error, Toast.LENGTH_SHORT).show()
                }
                return false
            }
            // Store edge-trimmed values — validation judges trimmed input,
            // so stored data and the switchTo lookup below stay consistent.
            // context prefs, NOT targetPrefs(): the bridge thread must not
            // depend on the fragment being attached (requireContext would
            // throw once detached).
            val prefs = context?.getSharedPreferences(PREFS_FILE, Context.MODE_PRIVATE)
                ?: return false
            val normalized = targets.map { RemoteTarget(it.name.trim(), it.host.trim(), it.port) }
            val editor = prefs.edit()
                .putString(KEY_REMOTES, serializeRemoteTargets(normalized))
            reconcileTargetAfterSave(
                prefs.getString(KEY_TARGET, TARGET_LOCAL) ?: TARGET_LOCAL,
                prefs.getString(KEY_CURRENT, "") ?: "",
                normalized,
            )?.let { editor.putString(KEY_TARGET, it) }
            editor.apply()
            // Re-inject so the wordmark label and popup context reflect a
            // renamed/deleted active endpoint immediately — the page itself
            // never re-fires onPageFinished after a settings save.
            activity?.runOnUiThread { applyTarget(webView) }
            return true
        }

        // Target switch from the header popup: "local" flips back to the
        // local daemon; anything else is looked up BY NAME in the endpoint
        // list. Unknown names toast no-op (stale popup). Either way the
        // WebView reloads to the new target's URL.
        @JavascriptInterface
        fun switchTo(name: String) {
            activity?.runOnUiThread {
                val prefs = targetPrefs()
                if (name == TARGET_LOCAL) {
                    prefs.edit().putString(KEY_TARGET, TARGET_LOCAL).apply()
                } else {
                    val match = remoteTargetsList().firstOrNull { it.name == name }
                    if (match == null) {
                        Toast.makeText(context, "未找到端点「$name」", Toast.LENGTH_SHORT).show()
                        return@runOnUiThread
                    }
                    prefs.edit()
                        .putString(KEY_TARGET, TARGET_REMOTE)
                        .putString(KEY_CURRENT, match.name)
                        .apply()
                }
                reloadToTarget()
            }
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
