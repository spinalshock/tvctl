package dev.tvctl.receiver

import android.content.Context
import android.net.wifi.WifiManager
import android.os.Bundle
import android.provider.Settings
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import java.net.Inet4Address
import java.net.NetworkInterface

/**
 * What the TV shows when you open the app: address, pairing state, and
 * which optional permissions are granted. Opening it once after install is
 * also what lets Fire OS deliver the boot broadcast later.
 */
class MainActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        ReceiverService.start(this)
    }

    override fun onResume() {
        super.onResume()
        visible = true
        // A request that arrived while we were in the background is parked
        // here; now that we're visible, Android lets us launch it.
        pending?.let { i ->
            pending = null
            startActivity(i)
            return
        }
        val prefs = App.instance.prefs
        val a11y = Settings.Secure.getString(contentResolver, Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES)
            ?.contains(packageName) == true
        val nl = Settings.Secure.getString(contentResolver, "enabled_notification_listeners")
            ?.contains(packageName) == true
        val lines = listOf(
            "tvctl receiver ${BuildConfig.VERSION_NAME}",
            "",
            "address      ${ip()}:${App.PORT}",
            "paired       ${if (prefs.token != null) "yes" else "no — run `tv setup` on your computer"}",
            "",
            "now playing  ${if (nl) "all apps" else "tvctl player only (tv setup unlocks all apps)"}",
            "keyboard     ${if (a11y) "events on" else "off (tv setup)"}",
            "screenshots  ${if (a11y && android.os.Build.VERSION.SDK_INT >= 30) "on" else "off (tv setup)"}",
            "overlay      ${if (Settings.canDrawOverlays(this)) "on" else "off"}",
        )
        findViewById<TextView>(R.id.status).text = lines.joinToString("\n")
    }

    override fun onPause() {
        visible = false
        super.onPause()
    }

    companion object {
        @Volatile var visible = false
        @Volatile var pending: android.content.Intent? = null
    }

    @Suppress("DEPRECATION")
    private fun ip(): String {
        runCatching {
            val wm = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
            val i = wm.connectionInfo.ipAddress
            if (i != 0) return "%d.%d.%d.%d".format(i and 0xff, i shr 8 and 0xff, i shr 16 and 0xff, i shr 24 and 0xff)
        }
        // Ethernet or anything else: first non-loopback IPv4.
        for (nif in NetworkInterface.getNetworkInterfaces()) {
            for (a in nif.inetAddresses) {
                if (a is Inet4Address && !a.isLoopbackAddress) return a.hostAddress ?: continue
            }
        }
        return "?"
    }
}
