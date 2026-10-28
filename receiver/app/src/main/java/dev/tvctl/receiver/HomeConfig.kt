package dev.tvctl.receiver

import android.content.Context
import android.content.pm.PackageManager
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * The home dashboard's settings, stored on the TV: whether it replaces
 * Amazon's home screen, the tiles in order, and the wallpaper file.
 *
 * A tile is an app (by package) or a TV input ("hdmi1".."hdmi4").
 */
object HomeConfig {
    data class Tile(val type: String, val id: String)

    val defaultTiles = listOf(
        Tile("input", "hdmi1"),
        Tile("app", "com.netflix.ninja"),
        Tile("app", "com.spotify.tv.android"),
        Tile("app", "org.smarttube.stable"),
        Tile("app", "com.amazon.firebat"),
    )

    /** Input tiles, by position: hdmi1 is the first HDMI input the TV lists. */
    val inputKeys = mapOf("hdmi1" to 1, "hdmi2" to 2, "hdmi3" to 3, "hdmi4" to 4)

    /** Called on the main thread after any change, so an open dashboard refreshes. */
    @Volatile var listener: (() -> Unit)? = null

    private fun sp(ctx: Context) = ctx.getSharedPreferences("home", Context.MODE_PRIVATE)

    fun enabled(ctx: Context) = sp(ctx).getBoolean("enabled", false)

    fun tiles(ctx: Context): List<Tile> {
        val raw = sp(ctx).getString("tiles", null) ?: return defaultTiles
        return runCatching {
            val a = JSONArray(raw)
            (0 until a.length()).map { a.getJSONObject(it).let { o -> Tile(o.getString("type"), o.getString("id")) } }
        }.getOrDefault(defaultTiles)
    }

    fun wallpaper(ctx: Context) = File(ctx.filesDir, "home_wallpaper")

    fun setEnabled(ctx: Context, on: Boolean) {
        sp(ctx).edit().putBoolean("enabled", on).apply()
        changed()
    }

    fun setTiles(ctx: Context, tiles: List<Tile>) {
        val a = JSONArray()
        tiles.forEach { a.put(JSONObject().put("type", it.type).put("id", it.id)) }
        sp(ctx).edit().putString("tiles", a.toString()).apply()
        changed()
    }

    fun changed() {
        val l = listener ?: return
        android.os.Handler(android.os.Looper.getMainLooper()).post(l)
    }

    /** Apps that wear another app's face on their tile: SmartTube is YouTube
     *  to the viewer, and the official YouTube app (disabled, not removed)
     *  still carries the real banner and name. */
    private val faces = mapOf("org.smarttube.stable" to "com.amazon.firetv.youtube")

    /** The package whose banner and name a tile shows: its face if that app
     *  is still on the TV (enabled or not), else the app itself. */
    fun face(ctx: Context, t: Tile): String {
        val f = faces[t.id] ?: return t.id
        return if (info(ctx, f) != null) f else t.id
    }

    fun info(ctx: Context, pkg: String) = runCatching {
        ctx.packageManager.getApplicationInfo(pkg, PackageManager.MATCH_DISABLED_COMPONENTS)
    }.getOrNull()

    fun label(ctx: Context, t: Tile): String = when (t.type) {
        "input" -> t.id.replace("hdmi", "HDMI ")
        else -> info(ctx, face(ctx, t))?.let { ctx.packageManager.getApplicationLabel(it).toString() } ?: t.id
    }

    fun installed(ctx: Context, t: Tile): Boolean = t.type != "app" || runCatching {
        ctx.packageManager.getApplicationInfo(t.id, 0); true
    }.getOrElse { it !is PackageManager.NameNotFoundException }

    fun toJson(ctx: Context): JSONObject {
        val a = JSONArray()
        tiles(ctx).forEach {
            a.put(JSONObject().put("type", it.type).put("id", it.id).put("label", label(ctx, it)).put("installed", installed(ctx, it)))
        }
        return JSONObject().put("enabled", enabled(ctx)).put("tiles", a).put("wallpaper", wallpaper(ctx).exists())
    }
}
