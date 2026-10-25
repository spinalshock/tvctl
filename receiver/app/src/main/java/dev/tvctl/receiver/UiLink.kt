package dev.tvctl.receiver

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import androidx.core.content.ContextCompat
import org.json.JSONObject

/**
 * The UI activities (dashboard, player) run in their own process, ":ui",
 * so the WebView and ExoPlayer libraries never load into the always-on
 * service, and the memory they take is returned when the screen closes.
 * This is the thin link between the two processes: in-package broadcasts
 * from the UI side, received here on the service side.
 */
object UiLink {
    const val ACTION_LOCAL_NOW = "dev.tvctl.receiver.LOCAL_NOW"   // player state, extra "json" or none to clear
    const val ACTION_DASH_STATE = "dev.tvctl.receiver.DASH_STATE" // extra "open"

    /** Whether a dashboard page is on screen, as last reported. */
    @Volatile var dashOpen = false

    /** Called from the UI process. */
    fun send(ctx: Context, action: String, fill: Intent.() -> Unit = {}) {
        ctx.sendBroadcast(Intent(action).setPackage(ctx.packageName).apply(fill))
    }

    /** Called once from the service process. */
    fun listen(ctx: Context, now: NowPlaying) {
        val r = object : BroadcastReceiver() {
            override fun onReceive(c: Context, i: Intent) {
                when (i.action) {
                    ACTION_LOCAL_NOW -> now.local = i.getStringExtra("json")?.let { runCatching { JSONObject(it) }.getOrNull() }
                    ACTION_DASH_STATE -> dashOpen = i.getBooleanExtra("open", false)
                }
            }
        }
        val f = IntentFilter().apply { addAction(ACTION_LOCAL_NOW); addAction(ACTION_DASH_STATE) }
        ContextCompat.registerReceiver(ctx, r, f, ContextCompat.RECEIVER_NOT_EXPORTED)
    }
}
