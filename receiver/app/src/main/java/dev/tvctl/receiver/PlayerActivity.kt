package dev.tvctl.receiver

import android.content.Intent
import android.os.Bundle
import android.view.KeyEvent
import androidx.appcompat.app.AppCompatActivity
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MediaMetadata
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.session.MediaSession
import androidx.media3.ui.PlayerView
import org.json.JSONObject

/**
 * Native playback for `tv play`. ExoPlayer decodes through the TV's
 * hardware codecs; the PlayerView gives transport controls that the remote
 * drives. A Media3 MediaSession makes this playback visible to the system
 * (and so to NowPlaying) exactly like Netflix's; we also report directly
 * over UiLink so now-playing works even before `tv setup`. Runs in the
 * ":ui" process (see UiLink).
 */
class PlayerActivity : AppCompatActivity() {
    private lateinit var player: ExoPlayer
    private lateinit var view: PlayerView
    private var session: MediaSession? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_player)
        view = findViewById(R.id.player)
        player = ExoPlayer.Builder(this)
            // handleAudioFocus=true: when another app starts playing, the
            // system takes focus from us and ExoPlayer pauses itself.
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(C.USAGE_MEDIA)
                    .setContentType(C.AUDIO_CONTENT_TYPE_MOVIE)
                    .build(),
                true
            )
            .build()
        view.player = player
        view.keepScreenOn = true
        session = MediaSession.Builder(this, player).build()
        player.addListener(object : Player.Listener {
            override fun onEvents(p: Player, events: Player.Events) = report()
            // A dead source (the serving machine went away) should not leave
            // an errored session sitting in the now-playing list.
            override fun onPlayerError(error: androidx.media3.common.PlaybackException) {
                finish()
            }
        })
        load(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        load(intent)
    }

    private fun load(i: Intent) {
        val url = i.getStringExtra("url") ?: return
        val title = i.getStringExtra("title").orEmpty().ifEmpty { url.substringAfterLast('/') }
        val item = MediaItem.Builder()
            .setUri(url)
            .setMediaMetadata(MediaMetadata.Builder().setTitle(title).build())
            .build()
        player.setMediaItem(item, i.getLongExtra("position", 0))
        player.prepare()
        player.playWhenReady = true
    }

    private fun report() {
        val md = player.mediaMetadata
        val state = JSONObject()
            .put("app", packageName)
            .put("title", md.title?.toString() ?: JSONObject.NULL)
            .put("state", when {
                player.playbackState == Player.STATE_BUFFERING -> "buffering"
                player.playbackState == Player.STATE_ENDED -> "stopped"
                player.isPlaying -> "playing"
                else -> "paused"
            })
            .put("position_ms", player.currentPosition)
            .put("duration_ms", player.duration.coerceAtLeast(0))
            .put("speed", if (player.isPlaying) 1.0 else 0.0)
            .put("updated_ms", System.currentTimeMillis())
        UiLink.send(this, UiLink.ACTION_LOCAL_NOW) { putExtra("json", state.toString()) }
    }

    /** Media keys on the Fire TV remote arrive as key events. */
    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean = when (keyCode) {
        KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE, KeyEvent.KEYCODE_DPAD_CENTER -> {
            if (player.isPlaying) player.pause() else player.play(); true
        }
        KeyEvent.KEYCODE_MEDIA_FAST_FORWARD, KeyEvent.KEYCODE_DPAD_RIGHT -> {
            player.seekTo(player.currentPosition + 10_000); true
        }
        KeyEvent.KEYCODE_MEDIA_REWIND, KeyEvent.KEYCODE_DPAD_LEFT -> {
            player.seekTo((player.currentPosition - 10_000).coerceAtLeast(0)); true
        }
        else -> super.onKeyDown(keyCode, event)
    }

    /** Leaving the screen (another app came to the front) pauses; a TV
     *  player has no background-audio use case. */
    override fun onStop() {
        player.pause()
        super.onStop()
    }

    override fun onDestroy() {
        session?.release()
        player.release()
        UiLink.send(this, UiLink.ACTION_LOCAL_NOW) // no json: clears
        super.onDestroy()
    }
}
