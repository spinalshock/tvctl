package dev.tvctl.receiver

import android.content.ComponentName
import android.content.Context
import android.media.MediaMetadata
import android.media.session.MediaController
import android.media.session.MediaSessionManager
import android.media.session.PlaybackState
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.service.notification.NotificationListenerService
import org.json.JSONObject

/**
 * Watches every media session on the TV. Netflix, YouTube, Spotify and
 * Prime all publish one (it's how the remote's play/pause reaches them),
 * carrying title, artist, artwork, position and state.
 *
 * Reading other apps' sessions needs notification-listener access, which
 * Fire OS has no UI for; `tv setup` grants it over ADB once. Without it we
 * still see our own player, because PlayerActivity reports directly.
 *
 * Android calls the callbacks on changes; nothing here polls.
 */
class NowPlaying(private val ctx: Context, private val events: Events) {
    private val msm = ctx.getSystemService(Context.MEDIA_SESSION_SERVICE) as MediaSessionManager
    private val main = Handler(Looper.getMainLooper())
    private val listenerComponent = ComponentName(ctx, NowPlayingListener::class.java)
    private val controllers = ArrayList<MediaController>()
    private val callbacks = HashMap<MediaController, MediaController.Callback>()

    /** Foreground package as last reported by the accessibility service. */
    @Volatile var foreground: String? = null
        set(v) {
            if (field != v) {
                field = v
                publish()
            }
        }

    /** Our own player's state, used when listener access is missing. */
    @Volatile var local: JSONObject? = null
        set(v) {
            field = v
            publish()
        }

    private val sessionsChanged = MediaSessionManager.OnActiveSessionsChangedListener { list ->
        attach(list ?: emptyList())
        publish()
    }

    fun hasListenerAccess(): Boolean {
        val enabled = Settings.Secure.getString(ctx.contentResolver, "enabled_notification_listeners") ?: return false
        return enabled.split(':').any { ComponentName.unflattenFromString(it) == listenerComponent }
    }

    fun start() {
        if (!hasListenerAccess()) return
        runCatching {
            msm.addOnActiveSessionsChangedListener(sessionsChanged, listenerComponent, main)
            attach(msm.getActiveSessions(listenerComponent))
        }
    }

    fun stop() {
        runCatching { msm.removeOnActiveSessionsChangedListener(sessionsChanged) }
        attach(emptyList())
    }

    private fun attach(list: List<MediaController>) {
        for ((c, cb) in callbacks) c.unregisterCallback(cb)
        callbacks.clear()
        controllers.clear()
        for (c in list) {
            val cb = object : MediaController.Callback() {
                override fun onPlaybackStateChanged(state: PlaybackState?) = publish()
                override fun onMetadataChanged(metadata: MediaMetadata?) = publish()
                override fun onSessionDestroyed() = publish()
            }
            c.registerCallback(cb, main)
            callbacks[c] = cb
            controllers.add(c)
        }
    }

    /** Pick the session that matters: playing beats paused beats idle;
     *  among equals, the first (most recently active per Android). */
    private fun best(): MediaController? {
        var pick: MediaController? = null
        var pickRank = -1
        for (c in controllers) {
            val r = when (c.playbackState?.state) {
                PlaybackState.STATE_PLAYING, PlaybackState.STATE_BUFFERING -> 3
                PlaybackState.STATE_PAUSED -> 2
                PlaybackState.STATE_NONE, null -> 0
                else -> 1
            }
            if (r > pickRank) {
                pick = c; pickRank = r
            }
        }
        return pick
    }

    /**
     * Transport control on the session that matters. Deterministic, unlike
     * the remote's play/pause key which only toggles. Returns false when
     * there is no session to control.
     */
    fun control(action: String, positionMs: Long): Boolean {
        val c = best() ?: return false
        val t = c.transportControls
        when (action) {
            "play" -> t.play()
            "pause" -> t.pause()
            "toggle" -> if (c.playbackState?.state == PlaybackState.STATE_PLAYING) t.pause() else t.play()
            "stop" -> t.stop()
            "next" -> t.skipToNext()
            "previous" -> t.skipToPrevious()
            "seek" -> t.seekTo(positionMs)
            "seek_by" -> {
                val ps = c.playbackState
                val now = (ps?.position ?: 0L) + if (ps?.state == PlaybackState.STATE_PLAYING)
                    ((android.os.SystemClock.elapsedRealtime() - (ps.lastPositionUpdateTime)) * ps.playbackSpeed).toLong() else 0L
                t.seekTo((now + positionMs).coerceAtLeast(0))
            }
            else -> return false
        }
        return true
    }

    fun snapshot(): JSONObject {
        val o = JSONObject().put("foreground", foreground ?: JSONObject.NULL)
        val c = best()
        if (c != null) {
            val md = c.metadata
            val ps = c.playbackState
            o.put("source", "session")
                .put("app", c.packageName)
                .put("title", md?.getString(MediaMetadata.METADATA_KEY_TITLE) ?: md?.getString(MediaMetadata.METADATA_KEY_DISPLAY_TITLE) ?: JSONObject.NULL)
                .put("artist", md?.getString(MediaMetadata.METADATA_KEY_ARTIST) ?: md?.getString(MediaMetadata.METADATA_KEY_DISPLAY_SUBTITLE) ?: JSONObject.NULL)
                .put("album", md?.getString(MediaMetadata.METADATA_KEY_ALBUM) ?: JSONObject.NULL)
                .put("duration_ms", md?.getLong(MediaMetadata.METADATA_KEY_DURATION) ?: 0L)
                .put("state", stateName(ps?.state))
                .put("position_ms", ps?.position ?: 0L)
                .put("speed", ps?.playbackSpeed?.toDouble() ?: 0.0)
                .put("updated_ms", ps?.lastPositionUpdateTime ?: 0L)
            return o
        }
        local?.let { l ->
            for (k in l.keys()) o.put(k, l.get(k))
            o.put("source", "tvctl")
            return o
        }
        return o.put("source", JSONObject.NULL).put("state", "idle")
    }

    private var lastKey: String? = null

    /** Publish only when something other than the position clock changed;
     *  callbacks fire several times per transition and clients extrapolate
     *  position themselves. */
    private fun publish() {
        val snap = snapshot()
        val key = JSONObject(snap.toString()).apply { remove("position_ms"); remove("updated_ms") }.toString()
        if (key == lastKey) return
        lastKey = key
        events.publish(JSONObject().put("type", "now").put("now", snap).toString())
    }

    private fun stateName(s: Int?) = when (s) {
        PlaybackState.STATE_PLAYING -> "playing"
        PlaybackState.STATE_PAUSED -> "paused"
        PlaybackState.STATE_BUFFERING -> "buffering"
        PlaybackState.STATE_STOPPED -> "stopped"
        PlaybackState.STATE_NONE, null -> "idle"
        else -> "other"
    }
}

/**
 * Deliberately empty. Declaring a NotificationListenerService is the only
 * way an app can be granted the access that MediaSessionManager checks.
 */
class NowPlayingListener : NotificationListenerService()
