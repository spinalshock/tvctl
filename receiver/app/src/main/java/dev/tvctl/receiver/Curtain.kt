package dev.tvctl.receiver

import android.app.Activity
import android.content.Context
import android.graphics.Bitmap
import android.graphics.PixelFormat
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.PixelCopy
import android.view.WindowManager
import android.widget.ImageView

/**
 * A still picture of the dashboard, laid over everything for a moment.
 *
 * Pressing Home on the dashboard makes the system start Amazon's home
 * screen (Fire OS never hands the Home key to apps), and our cover can only
 * bring the dashboard back once Amazon's home is already on screen, about
 * a third of a second later. The curtain hides that: when the dashboard is
 * paused for a reason it didn't cause, the frame on screen is copied and
 * shown in an overlay that takes no input, until the dashboard is back,
 * another screen appears, or a timeout passes. The picture is freed as soon
 * as the curtain lifts.
 */
object Curtain {
    private val main = Handler(Looper.getMainLooper())
    private var view: ImageView? = null
    private var bitmap: Bitmap? = null
    private var pending = false
    private val timeout = Runnable { hide() }

    fun show(a: Activity) {
        if (pending || view != null || Build.VERSION.SDK_INT < 26 || !Settings.canDrawOverlays(a)) return
        val d = a.window.decorView
        if (d.width == 0 || d.height == 0) return
        val bmp = Bitmap.createBitmap(d.width, d.height, Bitmap.Config.ARGB_8888)
        pending = true
        val app = a.applicationContext
        // Asynchronous: copying the frame never holds up the Home transition.
        PixelCopy.request(a.window, bmp, { result ->
            if (!pending || result != PixelCopy.SUCCESS) {
                bmp.recycle()
                pending = false
                return@request
            }
            pending = false
            put(app, bmp)
        }, main)
    }

    fun hide() {
        pending = false
        main.removeCallbacks(timeout)
        view?.let { v ->
            runCatching { (v.context.getSystemService(Context.WINDOW_SERVICE) as WindowManager).removeView(v) }
        }
        view = null
        bitmap?.recycle()
        bitmap = null
    }

    private fun put(ctx: Context, bmp: Bitmap) {
        val v = ImageView(ctx).apply { setImageBitmap(bmp); scaleType = ImageView.ScaleType.FIT_XY }
        val lp = WindowManager.LayoutParams(
            WindowManager.LayoutParams.MATCH_PARENT, WindowManager.LayoutParams.MATCH_PARENT,
            WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or WindowManager.LayoutParams.FLAG_NOT_TOUCHABLE or
                WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN or WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS,
            PixelFormat.OPAQUE,
        ).apply { windowAnimations = 0 }
        runCatching {
            (ctx.getSystemService(Context.WINDOW_SERVICE) as WindowManager).addView(v, lp)
            view = v
            bitmap = bmp
            main.postDelayed(timeout, 1_500)
        }.onFailure { bmp.recycle() }
    }
}
