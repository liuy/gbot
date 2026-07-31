package com.gbot.android

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.fragment.app.Fragment

class ChatFragment : Fragment() {

    companion object {
        private const val REQUEST_FILE_CHOOSER = 1001
        private const val REQUEST_CAPTURE = 1002
    }

    private var webView: WebView? = null
    private var loadAttempts = 0
    private val handler = Handler(Looper.getMainLooper())
    private var filePathCallback: ValueCallback<Array<Uri>>? = null

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
        // File input support: WebView does not open a file chooser by
        // default — without onShowFileChooser, tapping the attach (+) button
        // in WUI silently does nothing.
        webView?.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(
                webView: WebView?,
                filePathCallback: ValueCallback<Array<Uri>>?,
                fileChooserParams: FileChooserParams?
            ): Boolean {
                this@ChatFragment.filePathCallback?.onReceiveValue(null)
                this@ChatFragment.filePathCallback = filePathCallback
                val intent: Intent
                // capture attribute (camera icon) → open camera app directly;
                // createIntent() ignores capture and opens the generic file
                // picker instead.
                if (fileChooserParams?.isCaptureEnabled == true) {
                    intent = Intent(android.provider.MediaStore.ACTION_IMAGE_CAPTURE)
                    try {
                        startActivityForResult(intent, REQUEST_CAPTURE)
                    } catch (e: Exception) {
                        this@ChatFragment.filePathCallback = null
                        return false
                    }
                    return true
                }
                intent = fileChooserParams?.createIntent() ?: return false
                // createIntent() may narrow extension-only accept lists to
                // text/plain on some devices (WUI doc input uses
                // '.pdf,.doc,.zip,...'). Widen to */* so the picker shows
                // all files — backend validates extensions anyway.
                val acceptTypes = fileChooserParams?.acceptTypes
                if (acceptTypes?.any { it.startsWith(".") } == true) {
                    intent.type = "*/*"
                }
                try {
                    startActivityForResult(intent, REQUEST_FILE_CHOOSER)
                } catch (e: Exception) {
                    this@ChatFragment.filePathCallback = null
                    return false
                }
                return true
            }
        }

        webView?.webViewClient = object : WebViewClient() {
            override fun onReceivedError(view: WebView?, request: WebResourceRequest?, error: WebResourceError?) {
                if (request?.isForMainFrame() == true) {
                    retryLoad()
                }
            }
        }

        // Load immediately — onReceivedError retries if gbot hasn't bound
        // localhost:8765 yet.
        tryLoad()
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
        // Force localhost:8765 for local gbot — ignore any stale host/port
        // from the remote-server app version.
        webView?.visibility = View.VISIBLE
        webView?.loadUrl("http://localhost:8765/")
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        val callback = filePathCallback
        filePathCallback = null
        if (requestCode == REQUEST_CAPTURE) {
            // Camera returns the image as thumbnail in data?.data (some
            // devices) or in "data" extra (low-res). Full-res requires a
            // FileProvider URI passed via EXTRA_OUTPUT — for now accept
            // whatever the camera returns.
            val uri = data?.data
            callback?.onReceiveValue(if (resultCode == Activity.RESULT_OK && uri != null) arrayOf(uri) else null)
            return
        }
        if (requestCode == REQUEST_FILE_CHOOSER) {
            val results = if (resultCode == Activity.RESULT_OK && data?.data != null) {
                arrayOf(data.data!!)
            } else {
                null
            }
            callback?.onReceiveValue(results)
        }
    }

    override fun onDestroyView() {
        handler.removeCallbacksAndMessages(null)
        webView = null
        super.onDestroyView()
    }
}
