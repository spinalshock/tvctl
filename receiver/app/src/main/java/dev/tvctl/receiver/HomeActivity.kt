package dev.tvctl.receiver

import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.graphics.BitmapFactory
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.Drawable
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.StateListDrawable
import android.os.Bundle
import android.provider.Settings
import android.text.format.DateFormat
import android.view.Gravity
import android.view.KeyEvent
import android.view.View
import android.view.ViewGroup
import android.view.ViewOutlineProvider
import android.widget.FrameLayout
import android.widget.HorizontalScrollView
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import java.util.Date

/**
 * The home screen: a wallpaper, a clock, a settings gear, and one row of
 * tiles (apps and inputs) from HomeConfig. Shown instead of Amazon's home
 * screen when enabled (see TvctlAccessibilityService), and nothing else.
 *
 * Kept deliberately light: plain views, no frameworks, in the receiver's
 * main process (already running), so it appears without a cold start.
 *
 * Long-press OK on a tile to move it: it wiggles, left/right moves it along
 * the row, OK or Back drops it and saves the order.
 */
class HomeActivity : Activity() {
    private lateinit var bg: ImageView
    private lateinit var row: LinearLayout
    private lateinit var scroller: HorizontalScrollView
    private lateinit var name: TextView
    private lateinit var clock: TextView

    private var tiles = listOf<HomeConfig.Tile>()
    private var tileW = 0
    private var wallpaperStamp = 0L
    private var moving: View? = null
    private var wiggle: ObjectAnimator? = null
    private var swallowCenterUp = false

    private val tick = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) = updateClock()
    }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val root = FrameLayout(this)
        bg = ImageView(this).apply { scaleType = ImageView.ScaleType.CENTER_CROP }
        root.addView(bg, FrameLayout.LayoutParams(-1, -1))
        // A soft shade at the bottom keeps tiles and labels readable on any wallpaper.
        root.addView(View(this).apply {
            background = GradientDrawable(GradientDrawable.Orientation.TOP_BOTTOM,
                intArrayOf(Color.TRANSPARENT, Color.argb(170, 0, 0, 0)))
        }, FrameLayout.LayoutParams(-1, -1))

        // Top right: clock and settings.
        val top = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
        clock = TextView(this).apply {
            setTextColor(Color.WHITE); textSize = 22f; typeface = Typeface.create("sans-serif-light", Typeface.NORMAL)
        }
        val gear = ImageView(this).apply {
            setImageResource(R.drawable.ic_home_settings)
            setPadding(dp(8), dp(8), dp(8), dp(8))
            isFocusable = true
            background = StateListDrawable().apply {
                addState(intArrayOf(android.R.attr.state_focused),
                    GradientDrawable().apply { shape = GradientDrawable.OVAL; setColor(Color.argb(70, 255, 255, 255)) })
            }
            contentDescription = "Settings"
            setOnClickListener {
                TvctlAccessibilityService.settingsAt = android.os.SystemClock.uptimeMillis()  // don't cover Settings on its way
                startActivity(Intent(Settings.ACTION_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            }
        }
        top.addView(clock)
        top.addView(gear, LinearLayout.LayoutParams(dp(40), dp(40)).apply { marginStart = dp(16) })
        root.addView(top, FrameLayout.LayoutParams(-2, -2, Gravity.TOP or Gravity.END).apply {
            topMargin = dp(28); marginEnd = dp(40)
        })

        // The row of tiles, with the focused tile's name under it.
        row = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL; clipChildren = false; clipToPadding = false
            setPadding(dp(40), dp(24), dp(40), dp(24)) }
        scroller = HorizontalScrollView(this).apply { isHorizontalScrollBarEnabled = false; clipChildren = false
            clipToPadding = false; isFillViewport = false; addView(row) }
        name = TextView(this).apply { setTextColor(Color.WHITE); textSize = 17f; gravity = Gravity.CENTER }
        val shelf = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; gravity = Gravity.CENTER_HORIZONTAL; clipChildren = false }
        shelf.addView(scroller, LinearLayout.LayoutParams(-2, -2))
        shelf.addView(name, LinearLayout.LayoutParams(-1, -2))
        root.addView(shelf, FrameLayout.LayoutParams(-1, -2, Gravity.BOTTOM).apply { bottomMargin = dp(72) })
        root.clipChildren = false
        setContentView(root)

        load()
        HomeConfig.listener = { load() }
        registerReceiver(tick, IntentFilter(Intent.ACTION_TIME_TICK))
    }

    override fun onResume() {
        super.onResume()
        updateClock()
        if (currentFocus == null || currentFocus?.parent != row) row.getChildAt(0)?.requestFocus()
    }

    override fun onDestroy() {
        unregisterReceiver(tick)
        if (HomeConfig.listener != null) HomeConfig.listener = null
        super.onDestroy()
    }

    /** This is home: Back has nowhere to go. */
    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {}

    private fun updateClock() {
        clock.text = DateFormat.getTimeFormat(this).format(Date())
    }

    // ---- content -------------------------------------------------------

    /** (Re)builds from HomeConfig, keeping focus on the same tile. */
    private fun load() {
        val wf = HomeConfig.wallpaper(this)
        val stamp = if (wf.exists()) wf.lastModified() else 0L
        if (stamp != wallpaperStamp || bg.drawable == null) {
            wallpaperStamp = stamp
            bg.setImageDrawable(wallpaper())
        }
        val want = HomeConfig.tiles(this).filter { HomeConfig.installed(this, it) }
        if (want == tiles && row.childCount == want.size) return
        val focused = (currentFocus?.tag as? HomeConfig.Tile)
        tiles = want
        row.removeAllViews()
        // Fit the row to the screen (up to a comfortable maximum), leaving
        // room for the focused tile to grow; more tiles than fit scroll.
        val sideRoom = dp(40) * 2 + dp(48)
        val per = (resources.displayMetrics.widthPixels - sideRoom) / maxOf(want.size, 1) - dp(28)
        tileW = per.coerceIn(dp(120), dp(176))
        want.forEach { row.addView(tile(it)) }
        (row.children().firstOrNull { it.tag == focused } ?: row.getChildAt(0))?.requestFocus()
    }

    private fun wallpaper(): Drawable {
        val f = HomeConfig.wallpaper(this)
        if (f.exists()) {
            val dm = resources.displayMetrics
            val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
            BitmapFactory.decodeFile(f.path, bounds)
            var sample = 1
            while (bounds.outWidth / (sample * 2) >= dm.widthPixels && bounds.outHeight / (sample * 2) >= dm.heightPixels) sample *= 2
            val bmp = BitmapFactory.decodeFile(f.path, BitmapFactory.Options().apply {
                inSampleSize = sample; inPreferredConfig = android.graphics.Bitmap.Config.RGB_565
            })
            if (bmp != null) return android.graphics.drawable.BitmapDrawable(resources, bmp)
        }
        return GradientDrawable(GradientDrawable.Orientation.TL_BR,
            intArrayOf(Color.rgb(16, 18, 28), Color.rgb(28, 24, 44), Color.rgb(10, 10, 14)))
    }

    private fun tile(t: HomeConfig.Tile): View {
        val w = tileW; val h = tileW * 9 / 16  // 16:9, the shape of Android TV app banners
        val shape = GradientDrawable().apply { cornerRadius = dp(14).toFloat(); setColor(Color.rgb(30, 30, 38)) }
        val v = FrameLayout(this).apply {
            tag = t
            background = shape
            outlineProvider = ViewOutlineProvider.BACKGROUND
            clipToOutline = true
            isFocusable = true
            isClickable = true
            isLongClickable = true
        }
        val art = art(t)
        if (art != null) {
            v.addView(ImageView(this).apply { setImageDrawable(art); scaleType = ImageView.ScaleType.CENTER_CROP },
                FrameLayout.LayoutParams(-1, -1))
        } else {
            v.addView(TextView(this).apply {
                text = HomeConfig.label(this@HomeActivity, t); setTextColor(Color.WHITE); textSize = 20f
                typeface = Typeface.DEFAULT_BOLD; gravity = Gravity.CENTER
            }, FrameLayout.LayoutParams(-1, -1))
            if (t.type == "input") shape.colors = intArrayOf(Color.rgb(52, 56, 84), Color.rgb(26, 28, 44))
        }
        v.setOnFocusChangeListener { view, has ->
            val s = if (has) 1.12f else 1f
            view.animate().scaleX(s).scaleY(s).translationZ(if (has) dp(14).toFloat() else 0f).setDuration(140).start()
            if (has) {
                name.text = HomeConfig.label(this, t)
                view.post { scroller.smoothScrollTo(maxOf(0, view.left - dp(120)), 0) }
            }
        }
        v.setOnClickListener { open(t) }
        v.setOnLongClickListener { startMoving(it); true }
        return v.apply { layoutParams = LinearLayout.LayoutParams(w, h).apply { marginStart = dp(14); marginEnd = dp(14) } }
    }

    /** The app's TV banner, else its icon on the tile; inputs draw text.
     *  Rendered once at the focused tile's size: banners ship at full
     *  resolution, and keeping those around costs memory on the CPU and GPU. */
    private fun art(t: HomeConfig.Tile): Drawable? {
        if (t.type != "app") return null
        val pm = packageManager
        val d = runCatching {
            pm.getApplicationBanner(t.id)
                ?: pm.getLeanbackLaunchIntentForPackage(t.id)?.let { pm.getActivityBanner(it) }
                ?: pm.getApplicationIcon(t.id)
        }.getOrNull() ?: return null
        val w = (tileW * 1.12f).toInt()
        val h = w * 9 / 16
        val bmp = android.graphics.Bitmap.createBitmap(w, h, android.graphics.Bitmap.Config.ARGB_8888)
        val c = android.graphics.Canvas(bmp)
        val iw = d.intrinsicWidth.takeIf { it > 0 } ?: w
        val ih = d.intrinsicHeight.takeIf { it > 0 } ?: h
        val scale = maxOf(w.toFloat() / iw, h.toFloat() / ih)  // centre-crop
        val dw = (iw * scale).toInt(); val dh = (ih * scale).toInt()
        d.setBounds((w - dw) / 2, (h - dh) / 2, (w - dw) / 2 + dw, (h - dh) / 2 + dh)
        d.draw(c)
        return android.graphics.drawable.BitmapDrawable(resources, bmp)
    }

    private fun open(t: HomeConfig.Tile) {
        when (t.type) {
            "input" -> {
                val n = HomeConfig.inputKeys[t.id] ?: return
                startActivity(Intent(this, HdmiActivity::class.java).putExtra(HdmiActivity.EXTRA_PORT, n))
            }
            else -> {
                val i = packageManager.getLeanbackLaunchIntentForPackage(t.id)
                    ?: packageManager.getLaunchIntentForPackage(t.id)
                    ?: return toast("${HomeConfig.label(this, t)} isn't installed")
                startActivity(i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            }
        }
    }

    // ---- reordering ----------------------------------------------------

    private fun startMoving(v: View) {
        moving = v
        swallowCenterUp = true  // the release of this long press
        v.animate().scaleX(1.2f).scaleY(1.2f).setDuration(120).start()
        wiggle = ObjectAnimator.ofFloat(v, View.ROTATION, -1.6f, 1.6f).apply {
            duration = 110; repeatMode = ValueAnimator.REVERSE; repeatCount = ValueAnimator.INFINITE; start()
        }
        name.text = "Move with ◀ ▶ · OK to drop"
    }

    private fun stopMoving() {
        val v = moving ?: return
        moving = null
        wiggle?.cancel(); wiggle = null
        v.rotation = 0f
        v.animate().scaleX(1.12f).scaleY(1.12f).setDuration(120).start()
        tiles = row.children().mapNotNull { it.tag as? HomeConfig.Tile }.toList()
        HomeConfig.setTiles(this, tiles)
        name.text = HomeConfig.label(this, v.tag as HomeConfig.Tile)
    }

    private fun shift(v: View, by: Int) {
        val from = row.indexOfChild(v)
        val to = from + by
        if (to < 0 || to >= row.childCount) return
        row.removeViewAt(from)
        row.addView(v, to)
        v.requestFocus()
        v.post { scroller.smoothScrollTo(maxOf(0, v.left - dp(120)), 0) }
    }

    override fun dispatchKeyEvent(e: KeyEvent): Boolean {
        val m = moving ?: return super.dispatchKeyEvent(e)
        when (e.keyCode) {
            KeyEvent.KEYCODE_DPAD_LEFT, KeyEvent.KEYCODE_DPAD_RIGHT -> {
                if (e.action == KeyEvent.ACTION_DOWN) shift(m, if (e.keyCode == KeyEvent.KEYCODE_DPAD_LEFT) -1 else 1)
            }
            KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER -> {
                if (e.action == KeyEvent.ACTION_UP) {
                    if (swallowCenterUp) swallowCenterUp = false else stopMoving()
                }
            }
            KeyEvent.KEYCODE_BACK -> if (e.action == KeyEvent.ACTION_UP) stopMoving()
            KeyEvent.KEYCODE_DPAD_UP, KeyEvent.KEYCODE_DPAD_DOWN -> {}
            else -> return super.dispatchKeyEvent(e)
        }
        return true
    }

    private fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_LONG).show()

    private fun ViewGroup.children() = (0 until childCount).asSequence().map { getChildAt(it) }

    companion object {
        /** Brings the dashboard to the front (from the accessibility service). */
        fun show(ctx: Context) {
            ctx.startActivity(Intent(ctx, HomeActivity::class.java).addFlags(
                Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_NO_ANIMATION or Intent.FLAG_ACTIVITY_REORDER_TO_FRONT))
        }
    }
}
