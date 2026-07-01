package com.gbot.android

import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.fragment.app.Fragment
import androidx.preference.PreferenceManager

class ChatFragment : Fragment() {

    private var webView: WebView? = null

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?
    ): View {
        return inflater.inflate(R.layout.fragment_chat, container, false)
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        webView = view.findViewById(R.id.webView)
        webView?.settings?.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
        }
        webView?.webViewClient = WebViewClient()

        val prefs = PreferenceManager.getDefaultSharedPreferences(requireContext())
        val host = prefs.getString("host", "10.0.2.2") ?: "10.0.2.2"
        val port = prefs.getString("port", "8765") ?: "8765"
        webView?.loadUrl("http://$host:$port/")
    }

    override fun onDestroyView() {
        webView = null
        super.onDestroyView()
    }
}
