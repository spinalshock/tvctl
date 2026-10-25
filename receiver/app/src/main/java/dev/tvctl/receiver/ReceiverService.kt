package dev.tvctl.receiver

import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.IBinder
import android.util.Log

/**
 * The always-on half of the app. A foreground service (so Fire OS doesn't
 * reap it under memory pressure) that owns the HTTP/WebSocket server and
 * the now-playing watcher. On TV the notification is invisible; it exists
 * because foreground services must post one.
 */
class ReceiverService : Service() {
    private var server: HttpServer? = null
    private var nowPlaying: NowPlaying? = null

    override fun onCreate() {
        super.onCreate()
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE
        )
        val n = Notification.Builder(this, App.CHANNEL)
            .setSmallIcon(R.drawable.ic_launcher)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(getString(R.string.notif_text))
            .setContentIntent(open)
            .setOngoing(true)
            .build()
        startForeground(1, n)

        nowPlaying = NowPlaying(this, App.instance.events).also { it.start() }
        HttpServerHolder.now = nowPlaying
        UiLink.listen(this, nowPlaying!!)
        server = HttpServer(this, App.PORT, nowPlaying!!).also {
            try {
                // Timeout 0: WebSocket clients sit idle between events and
                // must not be dropped by the HTTP read timeout. Dead peers
                // are detected by the per-socket ping in HttpServer.
                it.start(0, false)
                Log.i(TAG, "listening on :${App.PORT}")
            } catch (e: Exception) {
                Log.e(TAG, "server failed", e)
            }
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int = START_STICKY

    override fun onDestroy() {
        server?.stop()
        nowPlaying?.stop()
        HttpServerHolder.now = null
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    companion object {
        private const val TAG = "tvctl"
        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, ReceiverService::class.java))
        }
    }
}
