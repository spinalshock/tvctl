package dev.tvctl.receiver

import android.content.Context
import android.graphics.Color
import android.graphics.PixelFormat
import android.graphics.Typeface
import android.os.Build
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.WindowManager
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONObject

/**
 * A text panel drawn over whatever is on screen: a terminal's worth of log
 * lines along the bottom, a sidebar, or the whole screen. It needs the
 * overlay permission `tv setup` grants (SYSTEM_ALERT_WINDOW) and nothing
 * else: no activity, no focus, so the remote keeps driving the app behind
 * it and video keeps playing.
 *
 * The panel holds a bounded ring of lines, newest at the bottom, in a
 * monospace face with wrapping off, so a log line is one row and long
 * lines run off the edge the way a terminal would truncate them. All
 * methods must run on the main thread (HttpServer posts them).
 */
object Overlay {
    private var root: LinearLayout? = null
    private var title: TextView? = null
    private var body: TextView? = null
    private val lines = ArrayDeque<String>()

    // Current look; re-applied when show() is called again with changes.
    private var pos = "bottom"
    private var maxLines = 12
    private var opacity = 0.85
    private var sizeSp = 18f
    private var width = 0.45 // of the screen, for left/right panels

    val visible get() = root != null

    /**
     * Creates the panel if needed and applies any settings present in cfg
     * ("pos", "max_lines", "opacity", "size", "width", "title"). Then sets
     * the text: "lines" (array) or "text" (newline-split) replace the
     * buffer, or extend it when "append" is true.
     */
    fun show(ctx: Context, cfg: JSONObject) {
        var relayout = root == null
        cfg.optString("pos").takeIf { it.isNotEmpty() && it != pos }?.let { pos = it; relayout = true }
        if (cfg.has("max_lines")) maxLines = cfg.optInt("max_lines", maxLines).coerceIn(1, 200)
        if (cfg.has("opacity")) opacity = cfg.optDouble("opacity", opacity).coerceIn(0.0, 1.0)
        if (cfg.has("size")) sizeSp = cfg.optDouble("size", sizeSp.toDouble()).toFloat().coerceIn(8f, 80f)
        if (cfg.has("width")) { width = cfg.optDouble("width", width).coerceIn(0.1, 1.0); relayout = true }

        if (relayout) build(ctx)
        val t = title!!
        val b = body!!

        if (cfg.has("title")) {
            val s = cfg.optString("title")
            t.text = s
            t.visibility = if (s.isEmpty()) View.GONE else View.VISIBLE
        }
        b.setTextSize(TypedValue.COMPLEX_UNIT_SP, sizeSp)
        b.maxLines = maxLines
        root!!.setBackgroundColor(Color.argb((opacity * 255).toInt(), 0, 0, 0))

        val incoming = ArrayList<String>()
        cfg.optJSONArray("lines")?.let { arr -> for (i in 0 until arr.length()) incoming.add(arr.optString(i)) }
        if (cfg.has("text")) incoming.addAll(cfg.optString("text").split('\n'))
        if (!cfg.optBoolean("append", false)) lines.clear()
        lines.addAll(incoming)
        while (lines.size > maxLines) lines.removeFirst()
        b.text = lines.joinToString("\n")
    }

    fun hide(ctx: Context) {
        root?.let { runCatching { wm(ctx).removeView(it) } }
        root = null; title = null; body = null
        lines.clear()
    }

    private fun build(ctx: Context) {
        root?.let { runCatching { wm(ctx).removeView(it) } }

        val pad = dp(ctx, 16)
        val col = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad / 2, pad, pad / 2)
        }
        val t = TextView(ctx).apply {
            setTextColor(Color.parseColor("#9A9A9A"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            typeface = Typeface.MONOSPACE
            visibility = View.GONE
            setPadding(0, 0, 0, pad / 3)
        }
        val b = TextView(ctx).apply {
            setTextColor(Color.parseColor("#E8E8E8"))
            typeface = Typeface.MONOSPACE
            setHorizontallyScrolling(true) // one line per row, no wrapping
            gravity = Gravity.BOTTOM
        }
        col.addView(t, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT))
        col.addView(b, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT))

        val dm = ctx.resources.displayMetrics
        val lp = WindowManager.LayoutParams(
            WindowManager.LayoutParams.MATCH_PARENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            if (Build.VERSION.SDK_INT >= 26) WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY
            else @Suppress("DEPRECATION") WindowManager.LayoutParams.TYPE_PHONE,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                WindowManager.LayoutParams.FLAG_NOT_TOUCHABLE or
                WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN,
            PixelFormat.TRANSLUCENT
        )
        when (pos) {
            "top" -> lp.gravity = Gravity.TOP or Gravity.FILL_HORIZONTAL
            "left", "right" -> {
                lp.gravity = (if (pos == "left") Gravity.START else Gravity.END) or Gravity.FILL_VERTICAL
                lp.width = (dm.widthPixels * width).toInt()
                lp.height = WindowManager.LayoutParams.MATCH_PARENT
            }
            "full" -> { lp.height = WindowManager.LayoutParams.MATCH_PARENT }
            else -> lp.gravity = Gravity.BOTTOM or Gravity.FILL_HORIZONTAL
        }
        wm(ctx).addView(col, lp)
        root = col; title = t; body = b
    }

    private fun wm(ctx: Context) = ctx.getSystemService(Context.WINDOW_SERVICE) as WindowManager
    private fun dp(ctx: Context, v: Int) = (v * ctx.resources.displayMetrics.density).toInt()
}
