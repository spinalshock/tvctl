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
 * the pairing token in the page.
 *
 * Runs in the ":ui" process (see UiLink). The service reaches it only
 * through intents: a new page, "js" to run in the open page, or "close".
 * It reports open/closed back with a broadcast.
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
        token = intent.getStringExtra("token").orEmpty()
        load(intent)
        UiLink.send(this, UiLink.ACTION_DASH_STATE) { putExtra("open", true) }
    }

    private var token = ""

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        load(intent)
    }

    private fun load(i: Intent) {
        if (i.getBooleanExtra("close", false)) {
            finish()
            return
        }
        i.getStringExtra("js")?.let { js ->
            web.evaluateJavascript(js, null)
            return
        }
        i.getStringExtra("token")?.let { token = it }
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

    override fun onKeyDown(keyCode: Int, event: KeyEvent?): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            finish()
            return true
        }
        return super.onKeyDown(keyCode, event)
    }

    override fun onDestroy() {
        UiLink.send(this, UiLink.ACTION_DASH_STATE) { putExtra("open", false) }
        web.destroy()
        super.onDestroy()
    }

    /** JavaScript bridge. Methods run on a WebView worker thread, so the
     *  local HTTP call to the service is allowed here. */
    private inner class Bridge {
        @JavascriptInterface
        fun now(): String = runCatching {
            val c = java.net.URL("http://127.0.0.1:${App.PORT}/now").openConnection() as java.net.HttpURLConnection
            c.connectTimeout = 1000; c.readTimeout = 1500
            c.setRequestProperty("X-Token", token)
            c.inputStream.bufferedReader().use { it.readText() }
        }.getOrDefault("{}")
    }
}
