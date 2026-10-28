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
 * What Android only hands to an accessibility service:
 *
 *  - which package is in front (window state changes),
 *  - when the TV wants text: a system keyboard window appearing, or an
 *    editable view taking focus (published as "keyboard" events),
 *  - what the TV's focused text field holds (InputSession, read-only,
 *    only when tvctl asks),
 *  - screenshots, on Android 11+.
 *
 * It reports into the running service's NowPlaying and the event bus.
 */
class TvctlAccessibilityService : AccessibilityService() {
    private var keyboardOpen = false
    private var lastForeground: String? = null

    // Covering Amazon's home screen. An ordinary Home press is covered at
    // once. But Amazon opens Settings by bringing its home screen up first
    // and launching Settings on top ~100 ms later, so when Settings may be on
    // its way (our gear was just pressed, or Alexa was just used) the cover
    // waits briefly and only goes ahead if home is still the screen.
    private val main = android.os.Handler(android.os.Looper.getMainLooper())
    private var launcherScreen: String? = null
    private val coverCheck = Runnable { if (launcherScreen == AMAZON_HOME) coverAmazonHome() }

    override fun onServiceConnected() {
        instance = this
        // Amazon's home may already be on screen (boot, or this app was just
        // updated): no window change will come for it, so check now.
        main.postDelayed({ coverAmazonHome() }, 500)
    }

    /** Puts the dashboard over Amazon's home screen if that's what is showing.
     *  Without an event to say which screen it is, the active window's title
     *  tells Amazon's home ("Home") from its Settings ("KFTV Launcher"). */
    fun coverAmazonHome() {
        if (!HomeConfig.enabled(this)) return
        val w = runCatching { windows.firstOrNull { it.isActive && it.type == AccessibilityWindowInfo.TYPE_APPLICATION } }.getOrNull() ?: return
        val root = w.root ?: return
        val pkg = root.packageName?.toString()
        root.recycle()
        if (pkg == AMAZON_HOME_PKG && w.title?.toString() == "Home") HomeActivity.show(this)
    }

    override fun onDestroy() {
        if (instance === this) instance = null
        super.onDestroy()
    }

    override fun onAccessibilityEvent(e: AccessibilityEvent) {
        val pkg = e.packageName?.toString()
        when (e.eventType) {
            AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED -> {
                // Overlays (Alexa's vizzini, system UI) are not the foreground app.
                if (pkg != null && !pkg.startsWith("com.android.systemui") && pkg != "com.amazon.vizzini" && pkg != lastForeground) {
                    lastForeground = pkg
                    App.instance.events.publish(JSONObject().put("type", "foreground").put("package", pkg).toString())
                    nowPlaying()?.foreground = pkg
                }
                checkKeyboard(pkg)
                // Our home dashboard covers Amazon's home screen, and only that
                // screen: Settings lives in the same app and stays reachable.
                if (pkg == "com.amazon.vizzini") alexaAt = android.os.SystemClock.uptimeMillis()
                // The curtain stays only while Amazon's home is on its way; any
                // other screen (Settings, Alexa, an app) lifts it at once.
                if (pkg != null && pkg != packageName && !(pkg == AMAZON_HOME_PKG && e.className?.toString() == AMAZON_HOME)) Curtain.hide()
                if (pkg == AMAZON_HOME_PKG) {
                    launcherScreen = e.className?.toString()
                    if (launcherScreen == AMAZON_HOME && HomeConfig.enabled(this)) {
                        main.removeCallbacks(coverCheck)
                        val now = android.os.SystemClock.uptimeMillis()
                        if (now - settingsAt < 2_000 || now - alexaAt < 15_000) main.postDelayed(coverCheck, 300)
                        else HomeActivity.show(this)
                    }
                } else if (pkg != null && !pkg.startsWith("com.android.systemui") && pkg != "com.amazon.vizzini") {
                    launcherScreen = null  // another app came forward
                }
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
        const val AMAZON_HOME_PKG = "com.amazon.tv.launcher"

        /** When our dashboard last asked for Settings (uptime ms). */
        @Volatile var settingsAt = -60_000L

        /** When Alexa's overlay last appeared (uptime ms). */
        @Volatile var alexaAt = -60_000L

        fun alexaRecent() = android.os.SystemClock.uptimeMillis() - alexaAt < 15_000
        const val AMAZON_HOME = "com.amazon.tv.launcher.ui.HomeActivity_vNext"

        @Volatile private var instance: TvctlAccessibilityService? = null

        /** The running service, or null when accessibility isn't enabled. */
        fun get(): TvctlAccessibilityService? = instance

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
