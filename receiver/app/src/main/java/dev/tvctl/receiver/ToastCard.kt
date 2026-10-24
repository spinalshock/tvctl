package dev.tvctl.receiver

import android.content.Context
import android.graphics.Color
import android.graphics.PixelFormat
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.TypedValue
import android.view.Gravity
import android.view.WindowManager
import android.widget.TextView
import android.widget.Toast

/**
 * Our own toast: a small card in the bottom-right corner, drawn through the
 * overlay permission so it shows over any app, including full-screen video.
 * Android's Toast is used only when the overlay permission is missing; the
 * system drops toasts from apps that aren't on screen, which the receiver
 * never is while something else is playing.
 */
object ToastCard {
    private val main = Handler(Looper.getMainLooper())
    private var view: TextView? = null
    private val hide = Runnable { remove() }

    /** Main thread only. */
    fun show(ctx: Context, text: String, long: Boolean) {
        if (!Settings.canDrawOverlays(ctx)) {
            Toast.makeText(ctx, text, if (long) Toast.LENGTH_LONG else Toast.LENGTH_SHORT).show()
            return
        }
        val dm = ctx.resources.displayMetrics
        val dp = { v: Int -> (v * dm.density).toInt() }
        val tv = view ?: TextView(ctx).apply {
            setTextColor(Color.parseColor("#F2F2F2"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
            setPadding(dp(20), dp(12), dp(20), dp(12))
            maxWidth = dm.widthPixels / 2
            background = GradientDrawable().apply {
                cornerRadius = dp(10).toFloat()
                setColor(Color.argb(230, 24, 28, 34))
                setStroke(dp(1), Color.argb(90, 255, 255, 255))
            }
            val lp = WindowManager.LayoutParams(
                WindowManager.LayoutParams.WRAP_CONTENT,
                WindowManager.LayoutParams.WRAP_CONTENT,
                if (Build.VERSION.SDK_INT >= 26) WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY
                else @Suppress("DEPRECATION") WindowManager.LayoutParams.TYPE_PHONE,
                WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                    WindowManager.LayoutParams.FLAG_NOT_TOUCHABLE or
                    WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN,
                PixelFormat.TRANSLUCENT
            ).apply {
                gravity = Gravity.BOTTOM or Gravity.END
                x = dp(40); y = dp(40)
            }
            wm(ctx).addView(this, lp)
            view = this
        }
        tv.text = text
        main.removeCallbacks(hide)
        main.postDelayed(hide, if (long) 6000L else 3000L)
    }

    private fun remove() {
        view?.let { v -> runCatching { wm(v.context).removeView(v) } }
        view = null
    }

    private fun wm(ctx: Context) = ctx.getSystemService(Context.WINDOW_SERVICE) as WindowManager
}
