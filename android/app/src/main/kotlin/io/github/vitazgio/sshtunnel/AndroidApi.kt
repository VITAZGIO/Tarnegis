package io.github.vitazgio.sshtunnel

import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Canvas
import android.net.Uri
import android.net.VpnService
import android.os.Handler
import android.os.Looper
import android.util.Base64
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.lang.ref.WeakReference
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/**
 * API главного экрана на телефоне — те же адреса и тот же JSON, что у окна ПК
 * (src/internal/webui, docs/API.md). Общий index.html ходит сюда через
 * сервер страниц ядра (mobile.StartUI → mobile.SetAPI), поэтому экран один
 * на обе платформы. Здесь только перевод запросов на язык телефона:
 * настройки (Settings), VPN-служба (TunnelService), список приложений.
 * Логику «куда» здесь не решаем — её решает ядро в службе.
 *
 * События экрану (поток /events) — mobile.Emit: core (две кнопки), stats,
 * log — в том же виде, что на ПК.
 */
object AndroidApi : mobile.API {

    /** Сколько серверов может быть в цепочке после своего (config.MaxExitHops). */
    private const val MAX_EXIT_HOPS = 2

    private lateinit var app: Context

    /** Строка на языке приложения (настройка языка), а не системы. */
    private fun str(id: Int, vararg a: Any): String = Lang.wrap(app).getString(id, *a)
    private val main = Handler(Looper.getMainLooper())

    /** Открытый экран: системный вопрос о VPN задаётся только с него. */
    interface Screen {
        fun askVpnPermission()
    }

    @Volatile var screen: WeakReference<Screen>? = null

    // Ошибка, которую ядро в службе не видит (не дали разрешение на VPN,
    // не заполнены настройки): показывается на круглой кнопке до следующего
    // нажатия.
    @Volatile private var localError = ""

    // Служба запускается не мгновенно: пока она не сообщила о себе, кнопка
    // уже «Подключаюсь…», а не выключена.
    @Volatile private var startingUntil = 0L
    @Volatile private var startingMesh = false
    @Volatile private var startingBypass = false

    private var seq = 0L
    private var lastCore = ""

    @Synchronized
    fun init(ctx: Context): Boolean {
        if (::app.isInitialized) return true
        app = ctx.applicationContext
        return try {
            mobile.Mobile.startUI(app.filesDir.absolutePath)
            mobile.Mobile.setAPI(this)
            TunnelService.onApiUpdate = { pushState() }
            TunnelService.onApiLog = { line -> emitLog(line) }
            true
        } catch (e: Exception) {
            false
        }
    }

    /** Адрес главного экрана для WebView. */
    fun mainUrl(): String {
        val s = Settings(app)
        val q = Uri.Builder()
            .appendQueryParameter("t", mobile.Mobile.uiToken())
            .appendQueryParameter("lang", if (s.language == "en") "en" else "ru")
        if (s.theme.isNotEmpty()) q.appendQueryParameter("theme", s.theme)
        return mobile.Mobile.startUI(app.filesDir.absolutePath) + "/index.html" + q.build().toString()
    }

    override fun handle(method: String, path: String, query: String, body: String): String = try {
        route(method, path, Uri.parse("http://local/?$query"), body)
    } catch (e: Exception) {
        err(e.message ?: e.toString())
    }

    private fun route(method: String, path: String, q: Uri, body: String): String {
        val s = Settings(app)
        val post = method == "POST"
        return when (path) {
            "/api" -> JSONObject().put("api", 1).put("platform", "android").toString()
            "/api/status" -> status(s).toString()
            "/api/state" -> {
                if (post) {
                    val o = obj(body)
                    want(s, if (o.has("tunnel")) o.getBoolean("tunnel") else null,
                        if (o.has("mesh")) o.getBoolean("mesh") else null)?.let {
                        return JSONObject().put("error", it).put("state", core(s)).toString()
                    }
                }
                core(s).toString()
            }
            "/api/start" -> wantResult(s, true, null)
            "/api/stop" -> wantResult(s, false, false)
            "/api/bypass" -> wantResult(s, obj(body).optBoolean("on", true), null)
            "/api/meshmode" -> wantResult(s, null, obj(body).optBoolean("on", true))
            "/api/config" -> if (post) saveConfig(s, obj(body)) else config(s).toString()
            "/api/profile/add" -> {
                val o = obj(body)
                val p = s.addProfile(o.optString("name"), o.optString("flag"))
                JSONObject().put("profile", profileJson(s, p)).put("config", config(s)).toString()
            }
            "/api/profile/remove" -> removeProfile(s, obj(body).optString("id"))
            "/api/profile/select" -> selectProfile(s, obj(body).optString("id"))
            "/api/profile/export" -> exportProfile(s, obj(body))
            "/api/profile/import" -> importText(s, body)
            "/api/profile/latency" -> latency(s)
            "/api/profile/exit" -> setExitVia(s, obj(body))
            "/api/key" -> saveKey(s, obj(body))
            "/api/settings/export" -> {
                val list = s.profiles
                JSONObject().put("data", SettingsBundle.build(s)).put("servers", list.size)
                    .put("keysMissing", list.count { !hasKey(s, it.id) }).toString()
            }
            "/api/settings/import" -> {
                val n = try {
                    SettingsBundle.apply(s, body)
                } catch (e: SettingsBundle.BadBundle) {
                    return err(Lang.wrap(app).getString(R.string.bundle_bad))
                }
                restartIfRunning(s)
                JSONObject().put("config", config(s)).put("servers", n)
                    .put("keysMissing", s.profiles.count { !hasKey(s, it.id) }).toString()
            }
            "/api/appprofiles" -> JSONObject().put("state", appProfilesState(AppProfiles(app, s))).toString()
            "/api/appprofiles/enable", "/api/appprofiles/add",
            "/api/appprofiles/switch", "/api/appprofiles/delete" -> appProfileAction(s, path, obj(body))
            // Смена ключа сервера (ТЗ 46): та же логика и JSON, что на ПК (Go).
            "/api/device" -> if (post) deviceOp(s, obj(body)) else deviceView(s, null).toString()
            "/api/hostkey" -> mobile.Mobile.hostKeyAPI(s.knownHostsFile.absolutePath, method,
                q.getQueryParameter("host") ?: "", q.getQueryParameter("port") ?: "", body)
            "/api/mesh" -> TunnelService.meshStatus()
            "/api/checkip" -> TunnelService.checkIP(app)
            "/api/speedtest" -> TunnelService.speedTest(app)
            "/api/selfcheck" -> TunnelService.selfCheck(app)
            "/api/serverusers" -> {
                val o = obj(body)
                TunnelService.serverUsers(app, o.optString("op"), o.optString("name"),
                    o.optString("deviceType"), o.optString("password"))
            }
            "/api/adblock/update" -> {
                val r = mobile.Mobile.updateBlockLists(obj(body).optString("sources"), s.adBlockListFile.absolutePath)
                pushState()
                r
            }
            "/api/adblock/stats" -> mobile.Mobile.adStatsJSON()
            "/api/update/check" -> updateCheck()
            "/api/update/download" -> updateDownload()
            // Обновление сети устройств (ТЗ 40): решает Go (mobile/netupdate.go).
            "/api/update/net" -> TunnelService.netUpdate(app, method, obj(body).optString("version"))
            "/api/update/now" -> TunnelService.netUpdateNow(app, obj(body).optString("version"))
            "/api/update/auto" -> {
                val o = obj(body)
                if (!o.has("on")) err("on?") else {
                    s.autoUpdate = o.getBoolean("on")
                    s.autoUpdateT = System.currentTimeMillis() / 1000
                    TunnelService.pushAutoUpdate(s)
                    JSONObject().put("ok", true).put("state", JSONObject(TunnelService.netUpdateState())).toString()
                }
            }
            "/api/processes" -> apps()
            "/api/appicon" -> appIcon(q.getQueryParameter("pkg") ?: q.getQueryParameter("path") ?: "")
            else -> err(Lang.wrap(app).getString(R.string.api_not_on_phone))
        }
    }

    // ---------------------------------------------------------------------
    // Две кнопки
    // ---------------------------------------------------------------------

    private fun running() = TunnelService.state.let { it == "connecting" || it == "connected" || it == "reconnecting" }

    /** Состояние ядра (core.State) из того, что знает служба. */
    private fun core(s: Settings): JSONObject {
        val st = TunnelService.state
        val run = running()
        val meshOk = s.meshConfigured()
        val starting = !run && st != "error" && System.currentTimeMillis() < startingUntil
        val bypass = if (starting) startingBypass else run && TunnelService.bypassOn
        val mesh = meshOk && (if (starting) startingMesh else run && TunnelService.meshOn)
        val linkUp = st == "connected"
        // «on» — только когда связь с сервером правда есть. При
        // переподключении раньше было «on»: сеть устройств горела зелёной,
        // хотя до сервера не доходило ничего.
        fun phase(on: Boolean) = when {
            !on -> "off"
            starting -> "connecting"
            linkUp -> "on"
            else -> "connecting"
        }
        val tunnel = JSONObject().put("want", bypass).put("phase", phase(bypass))
        val err = localError.ifEmpty { if (st == "error") TunnelService.detail else "" }
        if (err.isNotEmpty() && !bypass && !mesh) {
            tunnel.put("phase", "error").put("error", err)
            if (localError.isEmpty() && TunnelService.errorKind.isNotEmpty()) tunnel.put("errorKind", TunnelService.errorKind)
        }
        // Туннель поднят без сервера ради сети устройств (ТЗ 11): большая
        // кнопка гаснет с ошибкой сервера, как на ПК (core.ServerDownError) —
        // текст как есть и код причины, перевод — на экране.
        val down = TunnelService.serverDown
        if (run && !bypass && down.isNotEmpty() && localError.isEmpty()) {
            tunnel.put("phase", "error").put("error", down)
            if (TunnelService.serverDownKind.isNotEmpty()) tunnel.put("errorKind", TunnelService.serverDownKind)
        }
        // Сеть устройств без сервера работает — кнопка «включена», как на ПК:
        // ядро считает связь поднятой, а как она живёт — в meshLink.
        val ms = try { JSONObject(TunnelService.meshSummary) } catch (_: Exception) { JSONObject() }
        val meshLink = if (mesh && run) ms.optString("state").takeIf { it != "off" && it.isNotEmpty() } else null
        val meshLive = meshLink == "online" || meshLink == "local" || (meshLink != null && down.isNotEmpty())
        val m = JSONObject().put("want", mesh).put("phase", if (mesh && meshLive && !starting) "on" else phase(mesh))
        val mode = when {
            bypass && mesh -> "bypass+mesh"
            bypass -> "bypass"
            mesh -> "mesh"
            else -> "off"
        }
        val link = if (starting) "connecting" else st
        val out = JSONObject().put("tunnel", tunnel).put("mesh", m).put("meshAvailable", meshOk)
            .put("mode", mode).put("link", link)
            .put("detail", if (linkUp) s.active().host else "")
            .put("seq", seq)
        if (meshLink != null && m.optString("phase") == "on") {
            out.put("meshLink", meshLink)
            val direct = ms.optInt("direct")
            if (direct > 0) out.put("meshDirect", direct)
        }
        // Цепочка выхода (core.ExitPath, как на ПК) — пока туннель поднят.
        val exit = TunnelService.exitJson
        if (run && exit.isNotEmpty()) {
            try { out.put("exit", JSONObject(exit)) } catch (_: Exception) {}
        }
        return out
    }

    private fun wantResult(s: Settings, tunnel: Boolean?, mesh: Boolean?): String {
        want(s, tunnel, mesh)?.let { return err(it) }
        return JSONObject().put("ok", true).put("state", core(s)).toString()
    }

    /**
     * «Хочу» с экрана: tunnel/mesh — null, если эту кнопку не трогали.
     * Возвращает текст ошибки или null.
     */
    @Synchronized
    private fun want(s: Settings, tunnel: Boolean?, mesh: Boolean?): String? {
        localError = ""
        val run = running()
        val meshOk = s.meshConfigured()
        val curBypass = run && TunnelService.bypassOn
        val curMesh = run && meshOk && TunnelService.meshOn
        val b = tunnel ?: curBypass
        val m = meshOk && (mesh ?: curMesh)
        s.lastRunning = b
        s.lastMesh = m
        if (!b && !m) {
            startingUntil = 0
            if (TunnelService.state != "stopped") service(TunnelService.ACTION_STOP)
            pushState()
            return null
        }
        if (run) {
            service(TunnelService.ACTION_MODES, b, m)
            pushState()
            return null
        }
        if (!s.ready()) {
            localError = Lang.wrap(app).getString(R.string.need_settings)
            pushState()
            return localError
        }
        startingBypass = b
        startingMesh = m
        startingUntil = System.currentTimeMillis() + 20_000
        if (VpnService.prepare(app) != null) {
            // Разрешение на VPN спрашивает сама система — только с открытого
            // экрана. Ответ придёт в vpnAllowed.
            val a = screen?.get()
            if (a == null) {
                startingUntil = 0
                localError = Lang.wrap(app).getString(R.string.vpn_permission_needed)
                pushState()
                return localError
            }
            main.post { a.askVpnPermission() }
            pushState()
            return null
        }
        launch(b, m)
        return null
    }

    /** Ответ системы на вопрос о VPN (MainActivity). */
    fun vpnAllowed(ok: Boolean) {
        if (ok) {
            launch(startingBypass, startingMesh)
        } else {
            startingUntil = 0
            localError = Lang.wrap(app).getString(R.string.vpn_permission_denied)
            pushState()
        }
    }

    private fun launch(bypass: Boolean, mesh: Boolean) {
        if (bypass && mesh) service(TunnelService.ACTION_START) else service(TunnelService.ACTION_MODES, bypass, mesh)
        pushState()
    }

    private fun service(action: String, bypass: Boolean? = null, mesh: Boolean? = null) {
        val i = Intent(app, TunnelService::class.java).setAction(action)
        if (bypass != null) i.putExtra(TunnelService.EXTRA_BYPASS, bypass)
        if (mesh != null) i.putExtra(TunnelService.EXTRA_MESH, mesh)
        app.startService(i)
    }

    /** Режим при открытии приложения (Настройки → Запуск), как раньше. */
    fun applyStartMode() {
        val s = Settings(app)
        fun wanted(mode: String, last: Boolean) = when (mode) {
            "on" -> true
            "last" -> last
            else -> false
        }
        val bypass = wanted(s.startMode, s.lastRunning)
        val mesh = s.meshConfigured() && wanted(s.meshStartEffective, s.lastMesh)
        if ((bypass || mesh) && TunnelService.state == "stopped" && s.ready() && VpnService.prepare(app) == null) {
            launch(bypass, mesh)
        }
    }

    /** Служба что-то сообщила — экрану свежие кнопки и счётчики. */
    fun pushState() {
        if (!::app.isInitialized) return
        val s = Settings(app)
        if (running() || TunnelService.state == "error") startingUntil = 0
        synchronized(this) {
            val c = core(s)
            val key = c.toString()
            if (key != lastCore) {
                lastCore = key
                seq++
                c.put("seq", seq)
                mobile.Mobile.emit(JSONObject().put("kind", "core").put("core", c).toString())
            }
        }
        val stats = try { JSONObject(TunnelService.statsJson) } catch (_: Exception) { JSONObject() }
        if (stats.length() > 0) mobile.Mobile.emit(JSONObject().put("kind", "stats").put("stats", stats).toString())
    }

    private val isoTime = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US).apply {
        timeZone = TimeZone.getTimeZone("UTC")
    }

    private fun emitLog(line: String) {
        val level = if (line.contains("ошибк", true) || line.contains("error", true) || line.contains("fail", true)) "error" else "info"
        val time = synchronized(isoTime) { isoTime.format(Date()) }
        mobile.Mobile.emit(JSONObject().put("kind", "log").put("level", level).put("text", line)
            .put("time", time).toString())
    }

    private fun status(s: Settings): JSONObject {
        val stats = try { JSONObject(TunnelService.statsJson) } catch (_: Exception) { JSONObject() }
        val run = running()
        val st = if (System.currentTimeMillis() < startingUntil && !run) "connecting" else TunnelService.state
        return JSONObject()
            .put("state", st)
            .put("running", run)
            .put("config", config(s))
            .put("stats", stats)
            .put("os", "android")
            .put("effectiveProfile", s.activeProfileId)
            .put("version", BuildConfig.VERSION_NAME)
            .put("modes", JSONObject().put("bypass", run && TunnelService.bypassOn)
                .put("mesh", run && TunnelService.meshOn && s.meshConfigured())
                .put("meshConfigured", s.meshConfigured()))
            .put("adBlock", true)
            .put("adBlocked", s.adBlockTotal)
            .put("seenApps", JSONArray())
            .put("core", core(s))
    }

    // ---------------------------------------------------------------------
    // Настройки: тот же документ, что config.json на ПК
    // ---------------------------------------------------------------------

    private fun hasKey(s: Settings, id: String) = s.keyFile(id).let { it.exists() && it.length() > 0 }

    private fun lines(text: String) = JSONArray(text.split("\n").map { it.trim() }.filter { it.isNotEmpty() })

    private fun joinLines(a: JSONArray?): String =
        if (a == null) "" else (0 until a.length()).map { a.optString(it).trim() }.filter { it.isNotEmpty() }.joinToString("\n")

    private fun profileJson(s: Settings, p: Settings.Profile): JSONObject = JSONObject()
        .put("id", p.id).put("name", p.name).put("flag", p.flag)
        .put("host", p.host).put("sshPort", p.sshPort).put("user", p.user)
        // Ключ на телефоне — файл приложения, пути у человека нет: экран
        // показывает «ключ сохранён» и кнопку «Вставить ключ» (/api/key).
        .put("keyPath", "").put("keySaved", hasKey(s, p.id))
        .put("socksPort", 0).put("httpPort", 0)
        .put("poolSize", p.poolSize)
        .put("filterMode", p.filterMode).put("filterApps", JSONArray(p.filterApps.sorted()))
        .put("directHosts", lines(p.directHosts)).put("tunnelHosts", lines(p.tunnelHosts))
        .put("localViaTunnel", p.localViaTunnel)
        .put("panel", p.panel).put("deviceName", p.deviceName)
        .put("udpRelayEnabled", s.udpRelayEnabled)
        .put("meshEnabled", p.meshEnabled).put("meshKey", p.meshKey).put("meshName", p.meshName)
        .put("meshIncoming", p.meshIncoming).put("meshDirectOff", p.meshDirectOff)
        .put("meshOnlyThis", p.meshOnlyThis)
        .put("exitVia", JSONArray(p.exitVia))
        .put("fromPanel", p.fromPanel).put("hidden", p.hidden)

    private fun config(s: Settings): JSONObject {
        val arr = JSONArray()
        for (p in s.profiles) arr.put(profileJson(s, p))
        return JSONObject()
            .put("profiles", arr)
            .put("activeProfile", s.activeProfileId)
            .put("sysProxy", false).put("setEnvVars", false).put("verbose", false)
            .put("autoStart", s.startMode == "on").put("autoConnect", s.startMode == "on")
            .put("startMode", s.startMode).put("lastRunning", s.lastRunning)
            .put("meshStartMode", s.meshStartMode).put("lastMesh", s.lastMesh)
            .put("theme", s.theme).put("background", s.background)
            .put("adBlockEnabled", s.adBlockEnabled)
            .put("adBlockSources", s.adBlockSources).put("adBlockAllowlist", s.adBlockAllowlist)
            .put("language", s.language)
            .put("showServerPicker", s.showServerPicker)
            .put("autoPickFastest", false)
    }

    private fun applyProfile(p: Settings.Profile, o: JSONObject) {
        if (o.has("name")) p.name = o.optString("name")
        if (o.has("flag")) p.flag = o.optString("flag")
        // Сервер из панели (ТЗ 48): адрес, порт, вход и сеть устройств задаёт
        // панель и блок устройства, а не форма.
        val panel = p.fromPanel.isNotBlank()
        if (!panel && o.has("host")) p.host = o.optString("host").trim()
        if (!panel && o.optInt("sshPort") > 0) p.sshPort = o.optInt("sshPort")
        if (!panel && o.has("user")) p.user = o.optString("user").trim()
        if (o.optInt("poolSize") > 0) p.poolSize = o.optInt("poolSize")
        if (o.has("filterMode")) p.filterMode = o.optString("filterMode", "all").ifBlank { "all" }
        o.optJSONArray("filterApps")?.let { a ->
            p.filterApps = (0 until a.length()).map { a.optString(it).trim() }.filter { it.isNotEmpty() }.toMutableSet()
        }
        if (o.has("directHosts")) p.directHosts = joinLines(o.optJSONArray("directHosts"))
        if (o.has("tunnelHosts")) p.tunnelHosts = joinLines(o.optJSONArray("tunnelHosts"))
        if (o.has("localViaTunnel")) p.localViaTunnel = o.optBoolean("localViaTunnel")
        if (!panel && o.has("meshEnabled")) p.meshEnabled = o.optBoolean("meshEnabled")
        if (!panel && o.has("meshKey")) p.meshKey = o.optString("meshKey").trim()
        if (o.has("meshName")) p.meshName = o.optString("meshName").trim()
        if (o.has("meshIncoming")) p.meshIncoming = o.optBoolean("meshIncoming")
        if (o.has("meshDirectOff")) p.meshDirectOff = o.optBoolean("meshDirectOff")
        if (!panel && o.has("meshOnlyThis")) p.meshOnlyThis = o.optBoolean("meshOnlyThis")
    }

    /**
     * «Выход через» (ТЗ 20): проверка как на ПК (App.SetExitVia) — не сам
     * себя, без повторов, до двух, только существующие серверы с адресом.
     * Возвращает текст ошибки или null.
     */
    private fun exitViaError(s: Settings, id: String, via: List<String>): String? {
        if (via.size > MAX_EXIT_HOPS) return str(R.string.exit_too_many, MAX_EXIT_HOPS)
        val seen = mutableSetOf(id)
        for (v in via) {
            if (v == id) return str(R.string.exit_self)
            if (!seen.add(v)) return str(R.string.exit_twice)
            val hop = s.profiles.find { it.id == v } ?: return str(R.string.exit_not_found, v)
            if (hop.host.isBlank()) return str(R.string.exit_no_host, hop.name)
        }
        return null
    }

    private fun viaList(a: JSONArray?): List<String> =
        if (a == null) emptyList() else (0 until a.length()).map { a.optString(it).trim() }.filter { it.isNotEmpty() }

    /** POST /api/profile/exit {"id","via":[…]} — как на ПК (webui/exit.go). */
    private fun setExitVia(s: Settings, o: JSONObject): String {
        val id = o.optString("id").ifBlank { s.activeProfileId }
        val p = s.profiles.find { it.id == id } ?: return err(str(R.string.err_server_not_found))
        val via = viaList(o.optJSONArray("via"))
        exitViaError(s, id, via)?.let { return err(it) }
        p.exitVia = via.toMutableList()
        s.saveProfile(p)
        TunnelService.profilesChanged(s)
        pushState()
        val r = JSONObject().put("ok", true).put("config", config(s))
        if (running() && id == s.activeProfileId) r.put("note", Lang.wrap(app).getString(R.string.applies_on_reconnect))
        return r.toString()
    }

    private fun saveConfig(s: Settings, doc: JSONObject): String {
        val arr = doc.optJSONArray("profiles")
        if (arr != null) {
            val list = s.profiles
            for (i in 0 until arr.length()) {
                val o = arr.optJSONObject(i) ?: continue
                val p = list.find { it.id == o.optString("id") } ?: continue
                applyProfile(p, o)
                // Цепочка из формы настроек — с той же проверкой, что у
                // /api/profile/exit; неверная не сохраняется.
                if (o.has("exitVia")) {
                    val via = viaList(o.optJSONArray("exitVia"))
                    exitViaError(s, p.id, via)?.let { return err(it) }
                    p.exitVia = via.toMutableList()
                }
                s.saveProfile(p)
                if (o.has("udpRelayEnabled")) s.udpRelayEnabled = o.optBoolean("udpRelayEnabled")
            }
        }
        if (doc.has("startMode")) s.startMode = doc.optString("startMode", "off")
        if (doc.has("meshStartMode")) s.meshStartMode = doc.optString("meshStartMode")
        if (doc.has("theme")) s.theme = doc.optString("theme")
        if (doc.has("background")) s.background = doc.optString("background")
        if (doc.has("language")) s.language = if (doc.optString("language") == "en") "en" else "ru"
        if (doc.has("showServerPicker")) s.showServerPicker = doc.optBoolean("showServerPicker")
        if (doc.has("adBlockEnabled")) s.adBlockEnabled = doc.optBoolean("adBlockEnabled")
        if (doc.has("adBlockSources")) s.adBlockSources = doc.optString("adBlockSources")
        if (doc.has("adBlockAllowlist")) s.adBlockAllowlist = doc.optString("adBlockAllowlist")
        // Дополнительные серверы сети устройств меняются сразу (цепочка — со
        // следующим подключением).
        TunnelService.profilesChanged(s)
        pushState()
        val r = JSONObject().put("ok", true).put("config", config(s))
        if (running()) r.put("note", Lang.wrap(app).getString(R.string.applies_on_reconnect))
        return r.toString()
    }

    private fun saveKey(s: Settings, o: JSONObject): String {
        val key = o.optString("key").trim()
        if (!key.startsWith("-----BEGIN")) return err(Lang.wrap(app).getString(R.string.key_bad))
        val id = o.optString("id").ifBlank { s.activeProfileId }
        if (s.profiles.none { it.id == id }) return err(str(R.string.err_server_not_found))
        s.saveKeyFor(id, key)
        return JSONObject().put("ok", true).put("config", config(s)).toString()
    }

    // ---------------------------------------------------------------------
    // Серверы
    // ---------------------------------------------------------------------

    /** Настройки сменились целиком — работающий туннель поднимается заново. */
    private fun restartIfRunning(s: Settings) {
        if (TunnelService.state == "stopped") return
        service(TunnelService.ACTION_STOP)
        // Служба останавливается не мгновенно: запуск — следующим сообщением.
        main.postDelayed({ if (Settings(app).ready()) launch(true, true) }, 900)
    }

    private fun selectProfile(s: Settings, id: String): String {
        if (s.profiles.none { it.id == id }) return err(str(R.string.err_server_not_found))
        if (id == s.activeProfileId) return JSONObject().put("config", config(s)).toString()
        val wasRunning = TunnelService.state != "stopped"
        s.setActive(id)
        restartIfRunning(s)
        val r = JSONObject().put("config", config(s))
        r.put("note", app.getString(if (wasRunning) R.string.server_switched_reconnecting else R.string.server_switched))
        return r.toString()
    }

    private fun removeProfile(s: Settings, id: String): String {
        val p = s.profiles.find { it.id == id } ?: return err(str(R.string.err_server_not_found))
        val wasActive = id == s.activeProfileId
        if (p.fromPanel.isNotBlank()) {
            // Сервер из панели — только если устройство удалено в панели
            // (ТЗ 49); убирает его и из устройства общий код Go.
            if (!deviceGone) return err(str(R.string.err_panel_server))
            deviceApply(s, deviceState(s, "remove").put("id", id))
        } else {
            s.removeProfile(id)
        }
        if (wasActive) {
            // Серверов не осталось — подключаться некуда: туннель остановить.
            if (s.profiles.isEmpty()) {
                if (TunnelService.state != "stopped") service(TunnelService.ACTION_STOP)
            } else restartIfRunning(s)
        }
        TunnelService.profilesChanged(s)
        configChanged()
        return JSONObject().put("config", config(s)).toString()
    }

    private fun exportProfile(s: Settings, o: JSONObject): String {
        val p = s.profiles.find { it.id == o.optString("id") } ?: return err(str(R.string.err_server_not_found))
        val withKey = o.optBoolean("includeKey", true) && hasKey(s, p.id)
        val key = if (withKey) s.keyFile(p.id).readText() else ""
        val data = mobile.Mobile.buildConfig(
            p.name, p.flag, p.host, p.sshPort.toLong(), p.user, p.poolSize.toLong(),
            p.filterMode, p.filterApps.joinToString("\n"), p.directHosts, p.localViaTunnel,
            withKey, key, p.panel, p.deviceName, p.meshKey,
        )
        return JSONObject().put("data", data).put("keyIncluded", withKey).toString()
    }

    /**
     * Сервер из текста (файл экспорта, ссылка sshtunnel:// или QR) — тот же
     * разбор ядром, что на ПК. Пустой текущий сервер (первый запуск)
     * заменяется новым, иначе новый добавляется рядом.
     */
    fun importText(s: Settings, text: String): String {
        val parsed = try {
            mobile.Mobile.parseConfig(text)
        } catch (e: Exception) {
            return err(e.message ?: Lang.wrap(app).getString(R.string.import_bad_apps))
        }
        val emptyActive = s.active().host.isBlank()
        val oldId = s.activeProfileId
        val p = s.addProfile(parsed.getName(), parsed.getFlag())
        p.host = parsed.getHost()
        p.sshPort = parsed.getSshPort().toInt().let { if (it > 0) it else 22 }
        p.user = parsed.getUser().ifBlank { "tunnel" }
        p.poolSize = parsed.getPoolSize().toInt().let { if (it > 0) it else 4 }
        p.filterMode = parsed.getFilterMode().ifBlank { "all" }
        p.filterApps = parsed.getFilterApps().split("\n").map { it.trim() }.filter { it.isNotBlank() }.toMutableSet()
        p.directHosts = parsed.getDirectHosts()
        p.localViaTunnel = parsed.getLocalViaTunnel()
        p.panel = parsed.getPanel()
        p.deviceName = parsed.getDeviceName()
        if (parsed.getMeshKey().isNotBlank()) {
            p.meshEnabled = true
            p.meshKey = parsed.getMeshKey()
        }
        s.saveProfile(p)
        val withKey = parsed.getKeyIncluded() && parsed.getKeyContents().isNotBlank()
        if (withKey) s.saveKeyFor(p.id, parsed.getKeyContents())
        if (emptyActive && TunnelService.state == "stopped") {
            s.setActive(p.id)
            if (oldId != p.id) s.removeProfile(oldId)
        }
        pushState()
        return JSONObject().put("ok", true).put("profile", JSONObject().put("id", p.id))
            .put("config", config(s)).put("keyImported", withKey).toString()
    }

    private fun latency(s: Settings): String {
        val list = s.profiles.filter { it.host.isNotBlank() }
        val results = arrayOfNulls<JSONObject>(list.size)
        val threads = list.mapIndexed { i, p ->
            Thread {
                val ms = mobile.Mobile.latency(p.host, p.sshPort.toLong())
                results[i] = JSONObject().put("id", p.id).put("ms", if (ms > 0) ms else 0)
                    .apply { if (ms < 0) put("error", str(R.string.err_no_answer)) }
            }.apply { start() }
        }
        threads.forEach { it.join(10_000) }
        return JSONObject().put("results", JSONArray(results.filterNotNull())).toString()
    }

    // ---------------------------------------------------------------------
    // Профили приложения
    // ---------------------------------------------------------------------

    private fun appProfilesState(ap: AppProfiles): JSONObject {
        val arr = JSONArray()
        for (p in ap.list) arr.put(JSONObject().put("id", p.id))
        return JSONObject().put("enabled", ap.enabled).put("active", ap.active).put("profiles", arr)
    }

    private fun appProfileAction(s: Settings, path: String, o: JSONObject): String {
        val ap = AppProfiles(app, s)
        val wasRunning = TunnelService.state != "stopped"
        val what = path.substringAfterLast('/')
        if (what != "enable" && wasRunning) service(TunnelService.ACTION_STOP)
        try {
            when (what) {
                "enable" -> ap.setEnabled(o.optBoolean("on"))
                "add" -> ap.add()
                "switch" -> ap.switchTo(o.optString("id"))
                "delete" -> ap.delete(o.optString("id"))
            }
        } catch (e: Exception) {
            return JSONObject().put("error", Lang.wrap(app).getString(R.string.bundle_bad)).put("state", appProfilesState(ap)).toString()
        }
        if (what != "enable" && wasRunning) {
            main.postDelayed({ if (Settings(app).ready()) launch(true, true) }, 900)
        }
        return JSONObject().put("state", appProfilesState(ap)).toString()
    }

    // ---------------------------------------------------------------------
    // Приложения телефона (выбор, какие вести через туннель)
    // ---------------------------------------------------------------------

    private fun apps(): String {
        val pm = app.packageManager
        val list = pm.getInstalledApplications(0)
            .filter { pm.getLaunchIntentForPackage(it.packageName) != null && it.packageName != app.packageName }
            .map { it.packageName to (try { pm.getApplicationLabel(it).toString() } catch (_: Exception) { it.packageName }) }
            .sortedBy { it.second.lowercase() }
        val arr = JSONArray()
        for ((pkg, label) in list) arr.put(JSONObject().put("name", pkg).put("label", label).put("path", ""))
        return JSONObject().put("processes", arr).toString()
    }

    private fun appIcon(pkg: String): String {
        if (pkg.isBlank()) return ""
        return try {
            val d = app.packageManager.getApplicationIcon(pkg)
            val size = 96
            val bmp = Bitmap.createBitmap(size, size, Bitmap.Config.ARGB_8888)
            val c = Canvas(bmp)
            d.setBounds(0, 0, size, size)
            d.draw(c)
            val out = ByteArrayOutputStream()
            bmp.compress(Bitmap.CompressFormat.PNG, 100, out)
            "png:" + Base64.encodeToString(out.toByteArray(), Base64.NO_WRAP)
        } catch (_: Exception) {
            ""
        }
    }

    // ---------------------------------------------------------------------
    // Обновление
    // ---------------------------------------------------------------------

    @Volatile private var lastUpdate: UpdateCheck.Result? = null

    private fun updateCheck(): String {
        val r = try {
            UpdateCheck.check(BuildConfig.VERSION_NAME)
        } catch (e: UpdateCheck.RateLimited) {
            return JSONObject().put("error", "rate limited").put("code", "update_rate_limited").toString()
        } catch (e: Exception) {
            return err(e.message ?: str(R.string.err_check_failed))
        }
        lastUpdate = r
        return JSONObject().put("result", JSONObject()
            .put("installed", r.installed).put("latest", r.latest)
            .put("newer", r.newer).put("ahead", r.ahead).put("unknown", false)
            .put("url", r.releaseUrl).put("assetUrl", r.apkUrl))
            .put("net", TunnelService.netDecision(r.installed, r.latest)).toString()
    }

    // Скачивание APK идёт в фоновом потоке: handle() — это один синхронный
    // запрос страницы, и если ждать в нём весь файл, кнопка час висит на
    // «скачиваю…» без единого признака жизни. Вместо этого отвечаем сразу
    // («начал») и шлём прогресс отдельными событиями kind:"update" — как
    // core/log/stats (см. emitLog выше), страница слушает их в /events.
    @Volatile private var downloading = false

    /**
     * Скачивает APK сама программа (она исключена из VPN, идёт к GitHub
     * напрямую) и открывает системный установщик.
     */
    private fun updateDownload(): String {
        val u = lastUpdate ?: return err(str(R.string.err_check_updates_first))
        if (u.apkUrl.isBlank()) return err(str(R.string.err_no_apk))
        if (downloading) return JSONObject().put("ok", true).toString()
        downloading = true
        Thread({
            val dir = java.io.File(app.cacheDir, "updates").apply { mkdirs() }
            dir.listFiles()?.forEach { it.delete() }
            val file = java.io.File(dir, "ssh_tunnel-${u.latest}.apk")
            val part = java.io.File(dir, file.name + ".part")
            try {
                val conn = java.net.URL(u.apkUrl).openConnection() as java.net.HttpURLConnection
                conn.connectTimeout = 15000
                conn.readTimeout = 30000
                conn.instanceFollowRedirects = true
                if (conn.responseCode != 200) throw Exception("HTTP ${conn.responseCode}")
                val total = conn.contentLengthLong
                var done = 0L
                var lastEmit = 0L
                conn.inputStream.use { inp ->
                    part.outputStream().use { out ->
                        val buf = ByteArray(64 * 1024)
                        while (true) {
                            val n = inp.read(buf)
                            if (n < 0) break
                            out.write(buf, 0, n)
                            done += n
                            val now = System.currentTimeMillis()
                            if (now - lastEmit >= 200) {
                                lastEmit = now
                                emitUpdateProgress(done, total, false, null)
                            }
                        }
                    }
                }
                if (total > 0 && done != total) throw Exception(str(R.string.err_download_cut, done, total))
                if (!part.renameTo(file)) throw Exception(str(R.string.err_save_file))
                emitUpdateProgress(done, total, true, null)
                main.post { installApk(file) }
            } catch (e: Exception) {
                part.delete()
                emitUpdateProgress(0, 0, true, e.message ?: str(R.string.err_download_failed))
            } finally {
                downloading = false
            }
        }, "update-download").start()
        return JSONObject().put("ok", true).toString()
    }

    private fun emitUpdateProgress(done: Long, total: Long, finished: Boolean, error: String?) {
        val o = JSONObject().put("kind", "update").put("done", done).put("total", total).put("finished", finished)
        if (error != null) o.put("error", error)
        mobile.Mobile.emit(o.toString())
    }

    private fun installApk(file: java.io.File) {
        try {
            val uri = androidx.core.content.FileProvider.getUriForFile(app, "${app.packageName}.files", file)
            app.startActivity(
                Intent(Intent.ACTION_VIEW)
                    .setDataAndType(uri, "application/vnd.android.package-archive")
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
            )
        } catch (_: Exception) {
        }
    }

    // ---------------------------------------------------------------------

    // ---------------------------------------------------------------------
    // Устройство по приглашению из панели (ТЗ 48). Решает общий код Go
    // (mobile.DeviceJoin / DeviceFetch / DeviceApply → config, invite) —
    // здесь только хранение и ответы экрану, как /api/device на ПК.

    @Volatile private var deviceSync = JSONObject()
    /** Последняя сверка: ни один сервер устройство не знает (удалено в панели). */
    @Volatile private var deviceGone = false

    private fun deviceState(s: Settings, op: String): JSONObject = JSONObject()
        .put("op", op)
        .put("profiles", JSONArray(s.coreProfilesJson()))
        .put("active", s.activeProfileId)
        .put("device", s.deviceJson.takeIf { it.isNotBlank() }?.let { JSONObject(it) } ?: JSONObject.NULL)
        .put("knownHosts", s.knownHostsFile.absolutePath)

    /** Применить решение Go и, если сменился активный сервер, переподключиться. */
    private fun deviceApply(s: Settings, st: JSONObject, restart: Boolean = false): Pair<JSONObject, String> {
        val out = JSONObject(mobile.Mobile.deviceApply(st.toString()))
        val ch = s.applyDeviceState(out.toString())
        if (ch.activeChanged || restart) restartIfRunning(s)
        // Новые серверы из профиля — сразу и в работающую службу (иначе
        // они появлялись только после переподключения, ТЗ 49), и на экран.
        else if (out.optBoolean("changed")) TunnelService.profilesChanged(s)
        if (out.optBoolean("changed")) configChanged()
        pushState()
        return Pair(out, ch.note)
    }

    /**
     * Настройки поменялись не от экрана (профиль устройства, сеть серверов):
     * экран перечитывает /api/config — пока туннель поднят, сам он их не
     * опрашивает (как событие config на ПК).
     */
    fun configChanged() {
        if (!::app.isInitialized) return
        mobile.Mobile.emit(JSONObject().put("kind", "config").toString())
    }

    private fun deviceView(s: Settings, applied: JSONObject?): JSONObject {
        val v = applied ?: try {
            JSONObject(mobile.Mobile.deviceApply(deviceState(s, "view").toString()))
        } catch (e: Exception) { JSONObject() }
        val d = v.optJSONObject("device")
        val dev: Any = if (d == null) JSONObject.NULL else JSONObject()
            .put("id", d.optString("id")).put("name", d.optString("name")).put("join", d.optString("join"))
            .put("rev", d.optLong("rev")).put("mesh", d.optBoolean("mesh")).put("frozen", d.optBoolean("frozen"))
            .put("servers", d.optJSONArray("servers") ?: JSONArray()).put("hidden", d.optJSONArray("hidden") ?: JSONArray())
        return JSONObject().put("device", dev)
            .put("net", v.optJSONObject("net") ?: JSONObject())
            .put("meshAllowed", v.optBoolean("meshAllowed", true))
            .put("netMissing", v.optBoolean("netMissing", false))
            .put("gone", deviceGone)
            .put("sync", deviceSync)
    }

    private fun deviceOp(s: Settings, o: JSONObject): String = try {
        when (o.optString("op")) {
            "parse" -> JSONObject(mobile.Mobile.inviteInfo(o.optString("invite"))).put("ok", true).toString()
            "join" -> {
                val keyDir = s.keyFile("x").parentFile!!.absolutePath
                val j = mobile.Mobile.deviceJoin(o.optString("invite"), o.optString("name"), keyDir,
                    s.knownHostsFile.absolutePath)
                val (out, note) = deviceApply(s, deviceState(s, "set").put("join", JSONObject(j)))
                val pe = JSONObject(j).optString("profileErr")
                deviceSync = JSONObject().put("t", System.currentTimeMillis() / 1000).put("err", pe)
                // Профиль сразу не прочитался — есть только сервер приглашения.
                // Остальные — как только профиль прочитается (своим SSH, не
                // дожидаясь подключения), а не при следующем подключении.
                if (pe.isNotBlank()) retryFirstProfile(s)
                deviceView(s, out).put("ok", true).put("note", note).toString()
            }
            "sync" -> deviceView(s, deviceSyncNow(s)).put("ok", true)
                .put("note", deviceSync.optString("note").ifBlank { deviceSync.optString("err") }).toString()
            "hide" -> {
                val (out, note) = deviceApply(s, deviceState(s, "hide").put("id", o.optString("id"))
                    .put("hidden", o.optBoolean("hidden")))
                deviceView(s, out).put("ok", true).put("note", note).toString()
            }
            "net" -> {
                val n = deviceView(s, null).optJSONObject("net") ?: JSONObject()
                if (o.has("on")) n.put("on", o.optBoolean("on"))
                if (o.has("onlyThis")) n.put("onlyThis", o.optBoolean("onlyThis"))
                if (o.has("exitVia")) n.put("exitVia", o.optJSONArray("exitVia") ?: JSONArray())
                // Сеть устройств меняется переподключением, как на ПК.
                val (out, note) = deviceApply(s, deviceState(s, "net").put("net", n), restart = true)
                deviceView(s, out).put("ok", true).put("note", note).toString()
            }
            else -> err(str(R.string.err_unknown_op))
        }
    } catch (e: Exception) {
        err(e.message ?: e.toString())
    }

    /**
     * Сверка профиля устройства с панелью (при подключении и раз в 10 минут —
     * TunnelService). Применяется, только если rev вырос (решает Go).
     */
    @Synchronized
    fun deviceSyncNow(s: Settings): JSONObject? {
        if (s.deviceJson.isBlank()) return null
        return try {
            val prof = mobile.Mobile.deviceFetch(s.deviceJson, s.coreProfilesJson(), s.knownHostsFile.absolutePath)
            val (out, note) = deviceApply(s, deviceState(s, "apply").put("profile", JSONObject(prof)))
            deviceGone = false
            deviceSync = JSONObject().put("t", System.currentTimeMillis() / 1000).put("note", note)
            if (note.isNotBlank()) emitLog(str(R.string.log_panel_profile, note))
            out
        } catch (e: Exception) {
            val msg = e.message ?: e.toString()
            deviceGone = msg.contains(mobile.Mobile.deviceGoneText())
            deviceSync = JSONObject().put("t", System.currentTimeMillis() / 1000).put("err", msg)
            null
        }
    }

    /** Первый профиль устройства: до 10 попыток раз в 30 секунд. */
    private fun retryFirstProfile(s: Settings) {
        Thread {
            for (i in 0 until 10) {
                try { Thread.sleep(30_000) } catch (_: InterruptedException) { return@Thread }
                if (s.deviceJson.isBlank()) return@Thread
                if (deviceSyncNow(s) != null) return@Thread
            }
        }.start()
    }

    private fun obj(body: String): JSONObject = try { JSONObject(body) } catch (_: Exception) { JSONObject() }

    private fun err(msg: String): String = JSONObject().put("error", msg).toString()
}
