package dev.tvctl.receiver

import android.accessibilityservice.AccessibilityService
import android.os.Build
import android.view.accessibility.AccessibilityNodeInfo
import android.view.accessibility.AccessibilityWindowInfo
import org.json.JSONObject

/**
 * Read-only: what the TV's focused text field holds, so tvctl's typing box
 * can match it exactly (including after tvctl restarts). Typing itself goes
 * through the Fire TV remote protocol; this only reads.
 *
 * Why the companion app is needed for this: the remote protocol reports an
 * empty field's grey placeholder as if it were text. Accessibility can tell
 * the two apart.
 *
 * Only read when tvctl asks, and only while a keyboard is up: reading the
 * screen on every change disturbed Amazon's launcher (Alexa "open settings"
 * stopped working). Reads use fresh nodes (refresh(), never the cache).
 *
 * Some screens (Fire OS network setup) give no app view input focus and
 * type through the keyboard's own focused preview box; that box is read
 * instead. Password fields are flagged and never read.
 */
object InputSession {

    fun snapshot(svc: AccessibilityService): JSONObject {
        val (prompt, step, imePresent) = imeInfo(svc)
        // No keyboard up: nothing to read, and no reason to walk the screen.
        if (!imePresent) return JSONObject().put("open", false).put("prompt", "").put("step", "")
            .put("text", "").put("hint", "").put("password", false).put("key", "")
        val field = focused(svc)
        val o = JSONObject().put("prompt", prompt).put("step", step)
        if (field == null) {
            return o.put("open", true).put("text", "").put("hint", "").put("password", false)
                .put("key", listOf(step, prompt).joinToString("|"))
        }
        try {
            val showingHint = Build.VERSION.SDK_INT >= 26 && field.isShowingHintText
            val hint = if (Build.VERSION.SDK_INT >= 26) field.hintText?.toString().orEmpty() else ""
            val text = if (showingHint || field.isPassword) "" else field.text?.toString().orEmpty()
            val r = android.graphics.Rect().also { field.getBoundsInScreen(it) }
            return o.put("open", true).put("text", text).put("hint", hint).put("password", field.isPassword)
                // Changes when focus moves to a different field.
                .put("key", listOf(field.packageName, field.viewIdResourceName, r.toShortString(), step, prompt, hint).joinToString("|"))
        } finally {
            field.recycle()
        }
    }

    /** The focused editable field in an app window, else the keyboard's
     *  focused preview box. */
    private fun focused(svc: AccessibilityService): AccessibilityNodeInfo? {
        svc.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)?.let { f ->
            f.refresh()
            if (f.isEditable && f.window?.type != AccessibilityWindowInfo.TYPE_INPUT_METHOD) return f
            f.recycle()
        }
        for (type in intArrayOf(AccessibilityWindowInfo.TYPE_APPLICATION, AccessibilityWindowInfo.TYPE_INPUT_METHOD)) {
            for (w in svc.windows) {
                if (w.type != type) continue
                val root = w.root ?: continue
                val hit = find(root) { it.isEditable && it.isFocused }
                root.recycle()
                if (hit != null) return hit
            }
        }
        return null
    }

    /** The keyboard's title and step counter ("Enter DNS 1", "4 of 5"),
     *  empty for keyboards without them, and whether a keyboard is up. */
    private fun imeInfo(svc: AccessibilityService): Triple<String, String, Boolean> {
        for (w in svc.windows) {
            if (w.type != AccessibilityWindowInfo.TYPE_INPUT_METHOD) continue
            val root = w.root ?: return Triple("", "", true)
            var prompt = ""
            var step = ""
            walk(root) { n ->
                val d = n.contentDescription?.toString().orEmpty()
                val t = n.text?.toString().orEmpty()
                if (step.isEmpty() && d.startsWith("Field ")) step = d.removePrefix("Field ")
                if (prompt.isEmpty() && t.isNotBlank() && !n.isEditable && n.className?.endsWith("TextView") == true) prompt = t
            }
            root.recycle()
            return Triple(prompt, step, true)
        }
        return Triple("", "", false)
    }

    private fun walk(n: AccessibilityNodeInfo, f: (AccessibilityNodeInfo) -> Unit) {
        n.refresh()
        f(n)
        for (i in 0 until n.childCount) {
            val c = n.getChild(i) ?: continue
            walk(c, f)
            c.recycle()
        }
    }

    private fun find(n: AccessibilityNodeInfo, pred: (AccessibilityNodeInfo) -> Boolean): AccessibilityNodeInfo? {
        n.refresh()
        if (pred(n)) return AccessibilityNodeInfo.obtain(n)
        for (i in 0 until n.childCount) {
            val c = n.getChild(i) ?: continue
            val hit = find(c, pred)
            c.recycle()
            if (hit != null) return hit
        }
        return null
    }
}
