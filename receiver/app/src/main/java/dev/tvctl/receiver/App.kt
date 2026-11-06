package dev.tvctl.receiver

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.SharedPreferences
import java.security.SecureRandom

/**
 * Process-wide singletons. Everything that must outlive an Activity lives
 * here or in the foreground service: the token store, the event bus, and the
 * notification channel the service needs to stay in the foreground.
 */
class App : Application() {
    lateinit var prefs: Prefs
    val events = Events()

    override fun onCreate() {
        super.onCreate()
        instance = this
        prefs = Prefs(getSharedPreferences("tvctl", Context.MODE_PRIVATE))
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL, getString(R.string.notif_channel), NotificationManager.IMPORTANCE_MIN)
        )
        // Whatever brings the process up (boot, the accessibility service or
        // the notification listener restarting after a low-memory kill), the
        // server comes with it: Fire OS doesn't always restart it on its own.
        runCatching { ReceiverService.start(this) }
    }

    companion object {
        const val CHANNEL = "tvctl"
        const val PORT = 8434
        lateinit var instance: App
            private set
    }
}

/** Pairing token plus the in-flight PIN. SharedPreferences is enough. */
class Prefs(private val sp: SharedPreferences) {
    var token: String?
        get() = sp.getString("token", null)
        set(v) = sp.edit().putString("token", v).apply()

    /** PIN shown on screen and the time it expires; null when none pending. */
    @Volatile var pendingPin: String? = null
    @Volatile var pinExpires: Long = 0

    fun newPin(): String {
        val pin = (1000 + SecureRandom().nextInt(9000)).toString()
        pendingPin = pin
        pinExpires = System.currentTimeMillis() + 120_000
        return pin
    }

    fun newToken(): String {
        val b = ByteArray(16).also { SecureRandom().nextBytes(it) }
        return b.joinToString("") { "%02x".format(it) }.also { token = it }
    }
}

/**
 * Minimal publish/subscribe for pushing JSON lines to connected event
 * sockets. Subscribers are called on the publisher's thread, so handlers
 * must be cheap and never block (the WebSocket send is buffered).
 */
class Events {
    private val subs = java.util.concurrent.CopyOnWriteArraySet<(String) -> Unit>()
    fun subscribe(f: (String) -> Unit) = subs.add(f)
    fun unsubscribe(f: (String) -> Unit) = subs.remove(f)
    fun publish(json: String) = subs.forEach { f -> runCatching { f(json) } }
}
