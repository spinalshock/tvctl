package dev.tvctl.receiver

import android.annotation.SuppressLint
import android.content.Intent
import android.graphics.Color
import android.os.Bundle
import android.view.KeyEvent
import android.view.WindowManager
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity

/**
 * Full-screen page the computer pushes: a clock, a build dashboard, a
 * Grafana board, anything a browser can draw. The page is either HTML sent
 * in the request (rendered as-is) or a URL the TV loads itself, so a page
 * served from the computer can refresh its own data.
 *
 * Pages can call `tvctl.now()` for the TV's now-playing document as JSON,
 * which is how the built-in clock shows what is playing without needing
 * the pairing token.
 */
class DashActivity : AppCompatActivity() {
    private lateinit var web: WebView

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        web = WebView(this).apply {
            setBackgroundColor(Color.parseColor("#101418"))
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.mediaPlaybackRequiresUserGesture = false
            settings.loadWithOverviewMode = true
            settings.useWideViewPort = true
            settings.mixedContentMode = android.webkit.WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
            // Keep navigation inside the WebView; never bounce to Silk.
            webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest) = false
            }
            addJavascriptInterface(Bridge(), "tvctl")
        }
        setContentView(web)
        current = this
        load(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        load(intent)
    }

    private fun load(i: Intent) {
        val html = i.getStringExtra("html")
        val url = i.getStringExtra("url")
        when {
            !html.isNullOrEmpty() ->
                // A base URL lets relative fetches in pushed HTML resolve
                // against the computer that sent it, when one is given.
                web.loadDataWithBaseURL(i.getStringExtra("base") ?: "http://tvctl.local/", html, "text/html", "utf-8", null)
            !url.isNullOrEmpty() -> web.loadUrl(url)
        }
    }

    /** Runs JavaScript in the current page; how the computer updates a
     *  dashboard in place instead of reloading it. */
    fun eval(js: String) = runOnUiThread { web.evaluateJavascript(js, null) }

    override fun onKeyDown(keyCode: Int, event: KeyEvent?): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            finish()
            return true
        }
        return super.onKeyDown(keyCode, event)
    }

    override fun onDestroy() {
        if (current === this) current = null
        web.destroy()
        super.onDestroy()
    }

    private inner class Bridge {
        @JavascriptInterface
        fun now(): String = HttpServerHolder.now?.snapshot()?.toString() ?: "{}"
    }

    companion object {
        @Volatile var current: DashActivity? = null

        fun close() {
            current?.let { a -> a.runOnUiThread { a.finish() } }
        }
    }
}
