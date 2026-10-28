package dev.tvctl.receiver

import android.app.Activity
import android.media.tv.TvContract
import android.media.tv.TvInputInfo
import android.media.tv.TvInputManager
import android.media.tv.TvView
import android.os.Bundle
import android.util.Log
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.TextView
import android.widget.Toast

/**
 * Full-screen HDMI input, through Android's TV Input Framework: a TvView
 * tuned to the input's passthrough channel. Fire OS keeps its own input
 * switching (and the HDMI keys) to Amazon's apps, but the framework is open
 * to any app. Back returns to the dashboard.
 */
class HdmiActivity : Activity() {
    private lateinit var tv: TvView
    private lateinit var status: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        tv = TvView(this)
        status = TextView(this).apply {
            setTextColor(android.graphics.Color.argb(200, 255, 255, 255)); textSize = 22f
            gravity = android.view.Gravity.CENTER; visibility = android.view.View.GONE
        }
        val label = "HDMI ${intent.getIntExtra(EXTRA_PORT, 1)}"
        setContentView(FrameLayout(this).apply {
            setBackgroundColor(android.graphics.Color.BLACK)
            addView(tv, FrameLayout.LayoutParams(-1, -1))
            addView(status, FrameLayout.LayoutParams(-1, -1))
        })
        tv.setCallback(object : TvView.TvInputCallback() {
            override fun onConnectionFailed(inputId: String) = fail("couldn't connect to $inputId")
            override fun onDisconnected(inputId: String) = fail("$inputId disconnected")
            override fun onVideoUnavailable(inputId: String, reason: Int) {
                Log.i(TAG, "video unavailable on $inputId, reason $reason")
                // Tuning/buffering are brief; anything else means nothing to show.
                if (reason != android.media.tv.TvInputManager.VIDEO_UNAVAILABLE_REASON_TUNING &&
                    reason != android.media.tv.TvInputManager.VIDEO_UNAVAILABLE_REASON_BUFFERING) {
                    status.text = "No signal on $label"
                    status.visibility = android.view.View.VISIBLE
                }
            }
            override fun onVideoAvailable(inputId: String) {
                Log.i(TAG, "video available on $inputId")
                status.visibility = android.view.View.GONE
            }
        })
        val id = inputFor(intent.getIntExtra(EXTRA_PORT, 1)) ?: return fail("no HDMI input found")
        Log.i(TAG, "tuning $id")
        tv.tune(id, TvContract.buildChannelUriForPassthroughInput(id))
    }

    override fun onStop() {
        super.onStop()
        tv.reset()  // release the input when leaving
        finish()
    }

    /** The n-th HDMI input (1-based), in the TV's own order (HW2 is HDMI 1). */
    private fun inputFor(n: Int): String? {
        val tim = getSystemService(TvInputManager::class.java) ?: return null
        val hdmi = tim.tvInputList.filter { it.type == TvInputInfo.TYPE_HDMI && it.parentId == null }.sortedBy { it.id }
        return hdmi.getOrNull(n - 1)?.id
    }

    private fun fail(why: String) {
        Log.w(TAG, why)
        Toast.makeText(this, "HDMI: $why", Toast.LENGTH_LONG).show()
        finish()
    }

    companion object {
        const val TAG = "tvctl-hdmi"
        const val EXTRA_PORT = "port"
    }
}
