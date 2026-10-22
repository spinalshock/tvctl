package dev.tvctl.receiver

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.widget.Toast
import fi.iki.elonen.NanoHTTPD
import fi.iki.elonen.NanoWSD
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.io.IOException

/**
 * The control API the Go CLI talks to. Plain HTTP on the LAN; a pairing
 * token (X-Token header) guards everything except /status and the pairing
 * routes. Responses are JSON. Errors are {"error": "..."} with 4xx/5xx.
 *
 * Routes:
 *   GET  /status            caps and pairing state (no auth)
 *   POST /pair              show a PIN on the TV (no auth)
 *   POST /pair/verify       {"pin"} -> {"token"} (no auth)
 *   GET  /now               now-playing document
 *   GET  /events            WebSocket: one JSON object per message
 *   GET  /apps              launchable apps, including sideloaded ones
 *   POST /launch            {"package"}
 *   POST /open              {"url"} VIEW intent -> whichever app claims it
 *   POST /intent            {"action","data","package","component"}
 *   POST /play              {"url","title"} native player
 *   POST /toast             {"text"}
 *   POST /overlay           {"lines":[..]|"text", "append", "title", "pos", "max_lines", "opacity", "size", "width"}
 *   DELETE /overlay         remove the panel
 *   POST /dash              {"html"|"url", "base"} full-screen page; {"js"} runs in the open page
 *   DELETE /dash            close the page
 *   POST /control           {"action": play|pause|toggle|stop|next|previous|seek|seek_by, "position_ms"}
 *   GET  /shot              PNG screenshot (needs accessibility, API 30+)
 */
class HttpServer(private val ctx: Context, port: Int, private val now: NowPlaying) : NanoWSD(port) {
    private val main = Handler(Looper.getMainLooper())
    private val prefs get() = App.instance.prefs

    // ---- routing -------------------------------------------------------

    override fun serveHttp(session: IHTTPSession): Response = try {
        route(session)
    } catch (e: Exception) {
        json(Response.Status.INTERNAL_ERROR, JSONObject().put("error", e.message ?: e.toString()))
    }

    private fun route(s: IHTTPSession): Response {
        val path = s.uri.trimEnd('/')
        // Open routes.
        when {
            s.method == Method.GET && path == "/status" -> return status()
            s.method == Method.POST && path == "/pair" -> return pairStart()
            s.method == Method.POST && path == "/pair/verify" -> return pairVerify(body(s))
        }
        // Everything else needs the token.
        val tok = prefs.token
        if (tok == null || s.headers["x-token"] != tok) {
            return json(Response.Status.FORBIDDEN, JSONObject().put("error", "not paired: POST /pair"))
        }
        return when {
            s.method == Method.GET && path == "/now" -> ok(now.snapshot())
            s.method == Method.GET && path == "/apps" -> apps()
            s.method == Method.POST && path == "/launch" -> launch(body(s))
            s.method == Method.POST && path == "/open" -> open(body(s))
            s.method == Method.POST && path == "/intent" -> intent(body(s))
            s.method == Method.POST && path == "/play" -> play(body(s))
            s.method == Method.POST && path == "/toast" -> toast(body(s))
            s.method == Method.POST && path == "/overlay" -> overlay(body(s))
            s.method == Method.DELETE && path == "/overlay" -> overlayHide()
            s.method == Method.POST && path == "/dash" -> dash(body(s))
            s.method == Method.DELETE && path == "/dash" -> dashClose()
            s.method == Method.POST && path == "/control" -> control(body(s))
            s.method == Method.GET && path == "/shot" -> shot()
            else -> json(Response.Status.NOT_FOUND, JSONObject().put("error", "no such route"))
        }
    }

    // ---- handlers ------------------------------------------------------

    private fun status(): Response {
        val a11y = Settings.Secure.getString(ctx.contentResolver, Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES)
            ?.contains(ctx.packageName) == true
        val caps = JSONObject()
            .put("nowPlaying", now.hasListenerAccess())
            .put("accessibility", a11y)
            .put("screenshot", a11y && android.os.Build.VERSION.SDK_INT >= 30)
            .put("overlay", Settings.canDrawOverlays(ctx))
        return ok(
            JSONObject()
                .put("name", "tvctl-receiver")
                .put("version", BuildConfig.VERSION_NAME)
                .put("paired", prefs.token != null)
                .put("caps", caps)
        )
    }

    private fun pairStart(): Response {
        val pin = prefs.newPin()
        main.post {
            ctx.startActivity(
                Intent(ctx, PairActivity::class.java)
                    .putExtra("pin", pin)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            )
        }
        return ok(JSONObject().put("ok", true))
    }

    private fun pairVerify(b: JSONObject): Response {
        val pending = prefs.pendingPin
        val pin = b.optString("pin")
        if (pending == null || System.currentTimeMillis() > prefs.pinExpires) {
            return json(Response.Status.BAD_REQUEST, JSONObject().put("error", "no PIN pending: POST /pair first"))
        }
        if (pin != pending) {
            return json(Response.Status.FORBIDDEN, JSONObject().put("error", "wrong PIN"))
        }
        prefs.pendingPin = null
        val tok = prefs.newToken()
        main.post { PairActivity.dismiss() }
        return ok(JSONObject().put("token", tok))
    }

    private fun apps(): Response {
        val pm = ctx.packageManager
        val arr = JSONArray()
        val seen = HashSet<String>()
        // TV apps register under LEANBACK_LAUNCHER; phone-style sideloads
        // only under LAUNCHER. Ask for both so nothing is missed.
        for (cat in listOf(Intent.CATEGORY_LEANBACK_LAUNCHER, Intent.CATEGORY_LAUNCHER)) {
            val q = Intent(Intent.ACTION_MAIN).addCategory(cat)
            for (ri in pm.queryIntentActivities(q, 0)) {
                val pkg = ri.activityInfo.packageName
                if (!seen.add(pkg)) continue
                arr.put(JSONObject().put("id", pkg).put("name", ri.loadLabel(pm).toString()))
            }
        }
        return ok(JSONObject().put("apps", arr))
    }

    private fun launch(b: JSONObject): Response {
        val pkg = b.optString("package")
        val pm = ctx.packageManager
        val i = pm.getLeanbackLaunchIntentForPackage(pkg) ?: pm.getLaunchIntentForPackage(pkg)
            ?: return json(Response.Status.NOT_FOUND, JSONObject().put("error", "no launchable activity in $pkg"))
        i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        return start(i, JSONObject().put("package", pkg))
    }

    /** VIEW the URL; Android picks the app that claims it (Netflix for
     *  netflix.com/title/..., YouTube for youtu.be, Silk otherwise). An
     *  optional "package" pins the target. */
    private fun open(b: JSONObject): Response {
        val url = b.optString("url").ifEmpty {
            return json(Response.Status.BAD_REQUEST, JSONObject().put("error", "url required"))
        }
        val i = Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        b.optString("package").takeIf { it.isNotEmpty() }?.let { i.setPackage(it) }
        return fire(i)
    }

    /** Escape hatch for anything the other routes don't cover. */
    private fun intent(b: JSONObject): Response {
        val i = Intent(b.optString("action").ifEmpty { Intent.ACTION_VIEW })
        b.optString("data").takeIf { it.isNotEmpty() }?.let { i.data = Uri.parse(it) }
        b.optString("package").takeIf { it.isNotEmpty() }?.let { i.setPackage(it) }
        b.optString("component").takeIf { it.isNotEmpty() }?.let {
            i.component = ComponentName.unflattenFromString(it)
        }
        b.optJSONObject("extras")?.let { ex ->
            for (k in ex.keys()) {
                when (val v = ex.get(k)) {
                    is Boolean -> i.putExtra(k, v)
                    is Int -> i.putExtra(k, v)
                    is Long -> i.putExtra(k, v)
                    else -> i.putExtra(k, v.toString())
                }
            }
        }
        i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        return fire(i)
    }

    private fun fire(i: Intent): Response {
        val target = i.resolveActivity(ctx.packageManager)
            ?: return json(Response.Status.NOT_FOUND, JSONObject().put("error", "no app handles $i"))
        return start(i, JSONObject().put("resolved", target.flattenToShortString()))
    }

    /**
     * Starts an activity on the caller's behalf. Android 10+ refuses
     * activity starts from a background service unless the app is visible
     * or holds the overlay permission (which `tv setup` grants). Without
     * it we park the intent and tell the client "needsForeground"; the
     * client then asks the TV's own API to bring this app to the front,
     * and MainActivity launches the parked intent from its onResume.
     */
    private fun start(i: Intent, extra: JSONObject): Response {
        extra.put("ok", true)
        if (Settings.canDrawOverlays(ctx) || MainActivity.visible) {
            main.post { ctx.startActivity(i) }
            return ok(extra)
        }
        MainActivity.pending = i
        return ok(extra.put("needsForeground", true))
    }

    private fun play(b: JSONObject): Response {
        val url = b.optString("url").ifEmpty {
            return json(Response.Status.BAD_REQUEST, JSONObject().put("error", "url required"))
        }
        val i = Intent(ctx, PlayerActivity::class.java)
            .putExtra("url", url)
            .putExtra("title", b.optString("title"))
            .putExtra("position", b.optLong("position", 0))
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        return start(i, JSONObject())
    }

    private fun toast(b: JSONObject): Response {
        val text = b.optString("text").ifEmpty {
            return json(Response.Status.BAD_REQUEST, JSONObject().put("error", "text required"))
        }
        val long = b.optBoolean("long", false)
        main.post { Toast.makeText(ctx, text, if (long) Toast.LENGTH_LONG else Toast.LENGTH_SHORT).show() }
        return ok(JSONObject().put("ok", true))
    }

    /** Feed the on-screen text panel. View work belongs to the main thread;
     *  the request waits for it so a failure (no overlay permission) is
     *  reported to the caller rather than logged on the TV. */
    private fun overlay(b: JSONObject): Response {
        if (!Settings.canDrawOverlays(ctx)) {
            return json(Response.Status.NOT_IMPLEMENTED, JSONObject().put("error", "overlay permission missing: run tv setup"))
        }
        val err = onMain { Overlay.show(ctx, b) }
        if (err != null) return json(Response.Status.INTERNAL_ERROR, JSONObject().put("error", err))
        return ok(JSONObject().put("ok", true).put("visible", true))
    }

    /** Show a page full screen, or update the one showing. "js" alone
     *  only makes sense with a page open; without one it is an error so
     *  a script learns its dashboard is gone. */
    private fun dash(b: JSONObject): Response {
        val js = b.optString("js")
        val html = b.optString("html")
        val url = b.optString("url")
        if (html.isEmpty() && url.isEmpty()) {
            if (js.isEmpty()) return json(Response.Status.BAD_REQUEST, JSONObject().put("error", "html, url or js required"))
            val a = DashActivity.current
                ?: return json(Response.Status.NOT_FOUND, JSONObject().put("error", "no dashboard open: send html or url first"))
            a.eval(js)
            return ok(JSONObject().put("ok", true))
        }
        val i = Intent(ctx, DashActivity::class.java)
            .putExtra("html", html)
            .putExtra("url", url)
            .putExtra("base", b.optString("base"))
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        return start(i, JSONObject())
    }

    private fun dashClose(): Response {
        DashActivity.close()
        return ok(JSONObject().put("ok", true))
    }

    private fun overlayHide(): Response {
        onMain { Overlay.hide(ctx) }
        return ok(JSONObject().put("ok", true).put("visible", false))
    }

    /** Runs f on the main thread and returns its error message, if any. */
    private fun onMain(f: () -> Unit): String? {
        val done = java.util.concurrent.CountDownLatch(1)
        var err: String? = null
        main.post {
            try { f() } catch (e: Exception) { err = e.message ?: e.toString() }
            done.countDown()
        }
        done.await(3, java.util.concurrent.TimeUnit.SECONDS)
        return err
    }

    private fun control(b: JSONObject): Response {
        val action = b.optString("action")
        val ok = now.control(action, b.optLong("position_ms", 0))
        if (!ok) return json(Response.Status.NOT_FOUND, JSONObject().put("error", "nothing is playing to control (or unknown action \"$action\")"))
        return ok(JSONObject().put("ok", true).put("action", action))
    }

    private fun shot(): Response {
        val png = TvctlAccessibilityService.screenshotPng(5000)
            ?: return json(Response.Status.NOT_IMPLEMENTED, JSONObject().put("error", "screenshot needs the accessibility service (tv setup) on Android 11+"))
        return newFixedLengthResponse(Response.Status.OK, "image/png", ByteArrayInputStream(png), png.size.toLong())
    }

    // ---- websocket -----------------------------------------------------

    override fun openWebSocket(handshake: IHTTPSession): WebSocket {
        val authed = prefs.token != null && handshake.headers["x-token"] == prefs.token
        return EventSocket(handshake, authed, now)
    }

    /** One connected event client. Sends the current now-playing state on
     *  connect, then every published event. */
    private class EventSocket(h: IHTTPSession, private val authed: Boolean, private val now: NowPlaying) : WebSocket(h) {
        // Publishers run on the main thread (accessibility and media-session
        // callbacks), and Android forbids socket writes there. One writer
        // thread per socket keeps order and keeps the publisher cheap.
        private val writer = java.util.concurrent.Executors.newSingleThreadExecutor()
        private val sub: (String) -> Unit = { msg -> enqueue(msg) }
        private var pinger: Thread? = null

        private fun enqueue(msg: String) {
            runCatching {
                writer.execute {
                    runCatching { send(msg) }.onFailure { android.util.Log.w("tvctl", "ws send failed", it) }
                }
            }
        }

        override fun onOpen() {
            if (!authed) {
                runCatching { close(WebSocketFrame.CloseCode.PolicyViolation, "not paired", false) }
                return
            }
            App.instance.events.subscribe(sub)
            enqueue(JSONObject().put("type", "now").put("now", now.snapshot()).toString())
            // A ping every 20 s keeps middleboxes from timing the socket out
            // and surfaces dead peers as send failures, which close us.
            pinger = Thread {
                while (isOpen) {
                    try {
                        Thread.sleep(20_000)
                        ping(ByteArray(0))
                    } catch (_: Exception) {
                        break
                    }
                }
            }.apply { isDaemon = true; start() }
        }

        override fun onClose(code: WebSocketFrame.CloseCode?, reason: String?, initiatedByRemote: Boolean) {
            App.instance.events.unsubscribe(sub)
            pinger?.interrupt()
            writer.shutdownNow()
        }

        override fun onMessage(message: WebSocketFrame) {
            // Clients may send "ping" to keep NATs/timeouts happy.
            if (message.textPayload == "ping") runCatching { send("pong") }
        }

        override fun onPong(pong: WebSocketFrame?) {}
        override fun onException(exception: IOException?) {}
    }

    // ---- helpers -------------------------------------------------------

    private fun body(s: IHTTPSession): JSONObject {
        val files = HashMap<String, String>()
        s.parseBody(files)
        val raw = files["postData"] ?: return JSONObject()
        return runCatching { JSONObject(raw) }.getOrDefault(JSONObject())
    }

    private fun ok(o: JSONObject) = json(Response.Status.OK, o)

    private fun json(st: Response.Status, o: JSONObject): Response =
        newFixedLengthResponse(st, "application/json", o.toString()).apply {
            addHeader("Cache-Control", "no-store")
        }
}
