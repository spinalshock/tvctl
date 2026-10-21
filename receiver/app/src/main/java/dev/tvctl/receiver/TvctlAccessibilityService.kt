package dev.tvctl.receiver

import android.accessibilityservice.AccessibilityService
import android.graphics.Bitmap
import android.os.Build
import android.view.Display
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityWindowInfo
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/**
 * Three things Android only hands to an accessibility service:
 *
 *  - which package is in front (window state changes),
 *  - when the TV wants text: a system keyboard window appearing, or an
 *    editable view taking focus (apps like YouTube draw their own keyboard),
 *  - screenshots, on Android 11+.
 *
 * It reports into the running service's NowPlaying and the event bus; it
 * never reads screen content (canRetrieveWindowContent is off).
 */
class TvctlAccessibilityService : AccessibilityService() {
    private var keyboardOpen = false
    private var lastForeground: String? = null

    override fun onServiceConnected() {
        instance = this
    }

    override fun onDestroy() {
        if (instance === this) instance = null
        super.onDestroy()
    }

    override fun onAccessibilityEvent(e: AccessibilityEvent) {
        val pkg = e.packageName?.toString()
        when (e.eventType) {
            AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED -> {
                if (pkg != null && pkg != packageName && !pkg.startsWith("com.android.systemui") && pkg != lastForeground) {
                    lastForeground = pkg
                    App.instance.events.publish(JSONObject().put("type", "foreground").put("package", pkg).toString())
                    nowPlaying()?.foreground = pkg
                }
                checkKeyboard(pkg)
            }
            AccessibilityEvent.TYPE_WINDOWS_CHANGED -> checkKeyboard(pkg)
            AccessibilityEvent.TYPE_VIEW_FOCUSED -> {
                val src = e.source ?: return
                val editable = src.isEditable
                src.recycle()
                if (editable) setKeyboard(true, pkg)
            }
        }
    }

    /** System keyboard visible = an input-method window exists. */
    private fun checkKeyboard(pkg: String?) {
        val ime = windows.any { it.type == AccessibilityWindowInfo.TYPE_INPUT_METHOD }
        if (ime || keyboardOpen) setKeyboard(ime, pkg)
    }

    private fun setKeyboard(open: Boolean, pkg: String?) {
        if (open == keyboardOpen) return
        keyboardOpen = open
        App.instance.events.publish(
            JSONObject().put("type", "keyboard").put("open", open).put("package", pkg ?: JSONObject.NULL).toString()
        )
    }

    override fun onInterrupt() {}

    private fun nowPlaying(): NowPlaying? = HttpServerHolder.now

    companion object {
        @Volatile private var instance: TvctlAccessibilityService? = null

        /** PNG of the default display, or null if unavailable. Blocks up to
         *  timeoutMs; DRM-protected content comes back black by design. */
        fun screenshotPng(timeoutMs: Long): ByteArray? {
            val svc = instance ?: return null
            if (Build.VERSION.SDK_INT < 30) return null
            val out = AtomicReference<ByteArray?>()
            val done = CountDownLatch(1)
            svc.takeScreenshot(Display.DEFAULT_DISPLAY, svc.mainExecutor, object : TakeScreenshotCallback {
                override fun onSuccess(r: ScreenshotResult) {
                    val hw = Bitmap.wrapHardwareBuffer(r.hardwareBuffer, r.colorSpace)
                    r.hardwareBuffer.close()
                    val sw = hw?.copy(Bitmap.Config.ARGB_8888, false)
                    hw?.recycle()
                    if (sw != null) {
                        val bos = ByteArrayOutputStream()
                        sw.compress(Bitmap.CompressFormat.PNG, 100, bos)
                        sw.recycle()
                        out.set(bos.toByteArray())
                    }
                    done.countDown()
                }

                override fun onFailure(errorCode: Int) = done.countDown()
            })
            done.await(timeoutMs, TimeUnit.MILLISECONDS)
            return out.get()
        }
    }
}

/** Lets the accessibility service reach the service's NowPlaying without a
 *  binder dance; both live in this one process. */
object HttpServerHolder {
    @Volatile var now: NowPlaying? = null
}
