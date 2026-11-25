package io.github.vitazgio.sshtunnel

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.UUID

/**
 * Настройки приложения: список серверов (профилей) и общие параметры.
 *
 * Модель повторяет компьютерную версию (src/internal/config/config.go) —
 * список профилей плюс id активного, вместо одного плоского набора полей.
 * У каждого профиля свой файл ключа в filesDir/keys — так смена активного
 * сервера не путает ключи между собой.
 *
 * Старый (версии 1) набор плоских настроек хранится в тех же
 * SharedPreferences под старыми именами ключей — при первом обращении после
 * обновления он превращается в единственный профиль, а файл ключа
 * id_ed25519 переезжает в личный файл этого профиля. Дальше эти плоские
 * ключи не используются.
 */
class Settings(context: Context) {

    data class Profile(
        var id: String,
        var name: String,
        var flag: String,
        var host: String,
        var sshPort: Int,
        var user: String,
        var poolSize: Int,
        var directHosts: String,
        var localViaTunnel: Boolean,
        var filterMode: String,
        var filterApps: MutableSet<String>,
        // panel/deviceName — заполняются только при импорте конфига,
        // выданного веб-панелью на VPS (internal/share, поля версии 2).
        // Сервер, настроенный руками, их не имеет — panel остаётся пустым,
        // и экран настроек ничего дополнительного не показывает.
        var panel: String = "",
        var deviceName: String = "",
        // Сеть устройств на этом сервере (см. MeshActivity, docs/MESH.md).
        // Входящие на телефоне по умолчанию выключены: к нему самому
        // ходить обычно незачем.
        var meshEnabled: Boolean = false,
        var meshKey: String = "",
        var meshName: String = "",
        var meshIncoming: Boolean = false,
        // Не пробовать прямые соединения между устройствами.
        var meshDirectOff: Boolean = false,
        /** «Всегда в туннель» — обратный directHosts список, общий для всех серверов. */
        var tunnelHosts: String = "",
        // Сеть устройств только через этот сервер: не держать соединений с
        // другими серверами той же сети (ТЗ 12, как Profile.MeshOnlyThis на ПК).
        var meshOnlyThis: Boolean = false,
        // «Выход через» (ТЗ 8 на ПК, ТЗ 20 на телефоне): id других серверов по
        // порядку, до двух; в интернет — только через последний.
        var exitVia: MutableList<String> = mutableListOf(),
        // Профиль добавлен программой сама — сервер из сети серверов
        // («id сети/id сервера», как Profile.MeshAuto на ПК, ТЗ 26). По нему
        // общий код узнаёт, что сервер убрали из сети, и выключает профиль.
        var meshAuto: String = "",
        // Сервер выдан панелью устройству (ТЗ 48, как Profile.FromPanel на
        // ПК): учётная запись устройства. Адрес, вход и сеть устройств —
        // из устройства (deviceJson), решает общий код Go (mobile.DeviceApply).
        // panelKey — «адрес:порт» в профиле устройства; hidden — скрыт у себя.
        var fromPanel: String = "",
        var panelKey: String = "",
        var hidden: Boolean = false,
    )

    private val appCtx = context.applicationContext ?: context
    private val prefs = context.getSharedPreferences("settings", Context.MODE_PRIVATE)
    private val filesDir = context.filesDir

    private fun newProfile(name: String, flag: String = ""): Profile = Profile(
        id = UUID.randomUUID().toString(),
        name = name, flag = flag, host = "", sshPort = 22, user = "tunnel", poolSize = 4,
        directHosts = "", localViaTunnel = false, filterMode = "all", filterApps = mutableSetOf(),
    )

    /**
     * Список серверов. Если он ещё пуст (первый запуск после обновления или
     * совсем новая установка) — заводится один профиль: из старых плоских
     * настроек, если они были, иначе просто пустой.
     */
    var profiles: MutableList<Profile>
        get() {
            val raw = prefs.getString("profiles", null)
            val list = if (raw != null) decodeProfiles(raw) else mutableListOf()
            // Пустой сохранённый список — законное состояние (ТЗ 49): человек
            // удалил все серверы. Заводим профиль, только если списка не было.
            if (raw == null) {
                val migrated = migrateLegacy()
                list.add(migrated)
                saveAll(list, migrated.id)
            }
            return list
        }
        private set(_) {}

    var activeProfileId: String
        get() = prefs.getString("activeProfileId", "") ?: ""
        private set(v) = prefs.edit().putString("activeProfileId", v).apply()

    /** Сервер, который сейчас используется для подключения. */
    fun active(): Profile {
        val list = profiles
        // Серверов нет — пустой профиль без id: подключаться некуда.
        return list.find { it.id == activeProfileId } ?: list.firstOrNull() ?: newProfile("").copy(id = "")
    }

    /** Делает сервер активным. Ничего не значащий id — без эффекта. */
    fun setActive(id: String) {
        if (profiles.any { it.id == id }) activeProfileId = id
    }

    fun addProfile(name: String, flag: String): Profile {
        val list = profiles
        val p = newProfile(name.ifBlank { freeDefaultName(list) }, flag)
        list.add(p)
        saveAll(list, activeProfileId.ifBlank { p.id })
        return p
    }

    /**
     * Имя для сервера без своего названия — «Server N» с наименьшим свободным
     * номером, а не просто «на один больше, чем сейчас в списке». Иначе после
     * «завёл второй, удалил первый» следующий добавленный снова назывался бы
     * «Server 2», и в списке оказывалось бы два сервера с одним именем.
     */
    private fun freeDefaultName(list: List<Profile>): String {
        val taken = list.map { it.name }.toSet()
        var n = 1
        while ("Server $n" in taken) n++
        return "Server $n"
    }

    /** Удаляет сервер — и последний тоже (ТЗ 49): тогда серверов ноль. */
    fun removeProfile(id: String): Boolean {
        val list = profiles
        val gone = list.find { it.id == id } ?: return false
        list.removeAll { it.id == id }
        keyFile(id).delete()
        // Сеть серверов удалённый человеком сервер больше не возвращает
        // (ТЗ 50): отметки решает общий код Go.
        try {
            val marks = JSONArray(mobile.Mobile.removedMarks(JSONObject()
                .put("host", gone.host).put("sshPort", gone.sshPort)
                .put("meshAuto", gone.meshAuto).put("fromPanel", gone.fromPanel).toString()))
            if (marks.length() > 0) {
                val seen = try { JSONArray(meshAutoSeenJson) } catch (_: Exception) { JSONArray() }
                val have = (0 until seen.length()).map { seen.optString(it) }.toMutableSet()
                for (i in 0 until marks.length()) {
                    val m = marks.optString(i)
                    if (have.add(m)) seen.put(m)
                }
                meshAutoSeenJson = seen.toString()
            }
        } catch (_: Exception) {
        }
        val active = if (activeProfileId == id) list.firstOrNull()?.id.orEmpty() else activeProfileId
        saveAll(list, active)
        return true
    }

    /**
     * Полностью заменяет список серверов вставленной выгрузкой (см.
     * SettingsBundle). Каждому серверу выдаётся новый id и свой файл ключа;
     * старые файлы ключей удаляются, чтобы на телефоне не оставались
     * приватные ключи от серверов, которых в настройках уже нет.
     */
    fun replaceProfiles(incoming: List<Pair<Profile, String>>, activeIndex: Int) {
        if (incoming.isEmpty()) return
        for (old in profiles) keyFile(old.id).delete()

        val list = mutableListOf<Profile>()
        for ((p, key) in incoming) {
            val fresh = p.copy(id = UUID.randomUUID().toString(),
                name = p.name.ifBlank { "Server ${list.size + 1}" })
            list.add(fresh)
            if (key.isNotBlank()) saveKeyFor(fresh.id, key)
        }
        val active = list.getOrNull(activeIndex) ?: list.first()
        saveAll(list, active.id)
    }

    fun saveProfile(p: Profile) {
        if (p.id.isBlank()) return // пустой профиль «серверов нет» не сохраняется
        val list = profiles
        val i = list.indexOfFirst { it.id == p.id }
        if (i >= 0) list[i] = p else list.add(p)
        saveAll(list, activeProfileId.ifBlank { p.id })
    }

    /**
     * Программы через сервер, локальная сеть и «всегда напрямую» — общие для
     * всех серверов, как в «Настройках приложения» на компьютере: копируются
     * из [from] во все остальные.
     */
    fun shareTraffic(from: Profile) {
        val list = profiles
        for (p in list) {
            p.directHosts = from.directHosts
            p.tunnelHosts = from.tunnelHosts
            p.localViaTunnel = from.localViaTunnel
            p.filterMode = from.filterMode
            p.filterApps = from.filterApps.toMutableSet()
        }
        saveAll(list, activeProfileId.ifBlank { from.id })
    }

    private fun saveAll(list: List<Profile>, active: String) {
        prefs.edit()
            .putString("profiles", encodeProfiles(list))
            .putString("activeProfileId", active)
            .apply()
    }

    private fun encodeProfiles(list: List<Profile>): String {
        val arr = JSONArray()
        for (p in list) {
            val o = JSONObject()
            o.put("id", p.id); o.put("name", p.name); o.put("flag", p.flag)
            o.put("host", p.host); o.put("sshPort", p.sshPort); o.put("user", p.user)
            o.put("poolSize", p.poolSize); o.put("directHosts", p.directHosts)
            o.put("localViaTunnel", p.localViaTunnel); o.put("filterMode", p.filterMode)
            val apps = JSONArray()
            p.filterApps.forEach { apps.put(it) }
            o.put("filterApps", apps)
            o.put("panel", p.panel); o.put("deviceName", p.deviceName)
            o.put("meshEnabled", p.meshEnabled); o.put("meshKey", p.meshKey)
            o.put("meshName", p.meshName); o.put("meshIncoming", p.meshIncoming)
            o.put("meshDirectOff", p.meshDirectOff)
            o.put("tunnelHosts", p.tunnelHosts)
            o.put("meshOnlyThis", p.meshOnlyThis)
            o.put("exitVia", JSONArray(p.exitVia))
            o.put("meshAuto", p.meshAuto)
            o.put("fromPanel", p.fromPanel); o.put("panelKey", p.panelKey); o.put("hidden", p.hidden)
            arr.put(o)
        }
        return arr.toString()
    }

    private fun decodeProfiles(raw: String): MutableList<Profile> {
        val out = mutableListOf<Profile>()
        try {
            val arr = JSONArray(raw)
            for (i in 0 until arr.length()) {
                val o = arr.getJSONObject(i)
                val apps = mutableSetOf<String>()
                o.optJSONArray("filterApps")?.let { a ->
                    for (j in 0 until a.length()) apps.add(a.getString(j))
                }
                out.add(
                    Profile(
                        id = o.optString("id").ifBlank { UUID.randomUUID().toString() },
                        name = o.optString("name", "Server"),
                        flag = o.optString("flag", ""),
                        host = o.optString("host", ""),
                        sshPort = o.optInt("sshPort", 22),
                        user = o.optString("user", "tunnel"),
                        poolSize = o.optInt("poolSize", 4),
                        directHosts = o.optString("directHosts", ""),
                        localViaTunnel = o.optBoolean("localViaTunnel", false),
                        filterMode = o.optString("filterMode", "all"),
                        filterApps = apps,
                        panel = o.optString("panel", ""),
                        deviceName = o.optString("deviceName", ""),
                        meshEnabled = o.optBoolean("meshEnabled", false),
                        meshKey = o.optString("meshKey", ""),
                        meshName = o.optString("meshName", ""),
                        meshIncoming = o.optBoolean("meshIncoming", false),
                        meshDirectOff = o.optBoolean("meshDirectOff", false),
                        tunnelHosts = o.optString("tunnelHosts", ""),
                        meshOnlyThis = o.optBoolean("meshOnlyThis", false),
                        exitVia = o.optJSONArray("exitVia")?.let { a ->
                            (0 until a.length()).map { a.optString(it) }.filter { it.isNotBlank() }.toMutableList()
                        } ?: mutableListOf(),
                        meshAuto = o.optString("meshAuto", ""),
                        fromPanel = o.optString("fromPanel", ""),
                        panelKey = o.optString("panelKey", ""),
                        hidden = o.optBoolean("hidden", false),
                    )
                )
            }
        } catch (e: Exception) {
            // Повреждённый JSON — лучше начать с чистого листа, чем упасть.
        }
        return out
    }

    /**
     * Превращает старые (версии 1) плоские настройки в первый профиль, а файл
     * ключа id_ed25519 переносит в его личный файл. Если старых настроек не
     * было вовсе (чистая установка) — профиль просто пустой.
     */
    private fun migrateLegacy(): Profile {
        val p = newProfile("Server")
        val legacyHost = prefs.getString("host", null)
        if (legacyHost != null) {
            p.host = legacyHost
            p.sshPort = prefs.getInt("sshPort", 22)
            p.user = prefs.getString("user", "tunnel") ?: "tunnel"
            p.poolSize = prefs.getInt("poolSize", 4)
            p.directHosts = prefs.getString("directHosts", "") ?: ""
            p.localViaTunnel = prefs.getBoolean("localViaTunnel", false)
            p.filterMode = prefs.getString("filterMode", "all") ?: "all"
            p.filterApps = (prefs.getStringSet("filterApps", emptySet()) ?: emptySet()).toMutableSet()
        }
        val legacyKey = File(filesDir, "id_ed25519")
        if (legacyKey.exists() && legacyKey.length() > 0) {
            try {
                legacyKey.copyTo(keyFile(p.id), overwrite = true)
                applyKeyPermissions(keyFile(p.id))
                legacyKey.delete()
            } catch (e: Exception) {
                // Ключ не перенёсся — профиль останется без него, но остальные
                // настройки не теряются, ключ можно будет импортировать заново.
            }
        }
        return p
    }

    /**
     * Все серверы для ядра (mobile.Tunnel.SetProfiles) — в JSON профиля ПК
     * (config.Profile): кого держать дополнительным сервером сети устройств и
     * через кого выходить в интернет, решает общий код, как на ПК.
     */
    fun coreProfilesJson(): String {
        val arr = JSONArray()
        for (p in profiles) {
            arr.put(
                JSONObject()
                    .put("id", p.id).put("name", p.name).put("flag", p.flag)
                    .put("host", p.host).put("sshPort", p.sshPort).put("user", p.user)
                    .put("keyPath", keyFile(p.id).absolutePath)
                    .put("meshEnabled", p.meshEnabled).put("meshKey", p.meshKey)
                    .put("meshOnlyThis", p.meshOnlyThis)
                    .put("exitVia", JSONArray(p.exitVia))
                    .put("meshAuto", p.meshAuto)
                    .put("fromPanel", p.fromPanel).put("panelKey", p.panelKey).put("hidden", p.hidden)
            )
        }
        return arr.toString()
    }

    /**
     * Устройство из панели (ТЗ 48): JSON config.Device (учётная запись, ключ,
     * последний профиль от панели, скрытые серверы, сеть устройств). Пусто —
     * устройство не добавлялось по приглашению.
     */
    var deviceJson: String
        get() = prefs.getString("device", "") ?: ""
        set(v) = prefs.edit().putString("device", v).apply()

    /** Что сказать человеку после DeviceApply. */
    data class DeviceChange(val note: String, val activeChanged: Boolean)

    /**
     * Сохраняет ответ mobile.DeviceApply. Решал общий код Go (config.ApplyDevice
     * и соседи) — здесь только перенос: знакомые профили получают поля
     * входа и сети устройств, новые (серверы из панели) заводятся с ключом
     * устройства, убранные панелью — удаляются вместе со своим файлом ключа.
     */
    fun applyDeviceState(json: String): DeviceChange {
        val o = JSONObject(json)
        val arr = o.optJSONArray("profiles") ?: return DeviceChange("", false)
        val old = profiles
        val dev = o.optJSONObject("device")
        val devKey = dev?.optString("keyPath").orEmpty()
        val tmpl = old.find { it.id == activeProfileId } ?: old.firstOrNull()
        val list = mutableListOf<Profile>()
        for (i in 0 until arr.length()) {
            val po = arr.getJSONObject(i)
            val id = po.optString("id")
            if (id.isBlank()) continue
            val p = old.find { it.id == id }
                ?: tmpl?.let { t ->
                    t.copy(id = id, filterApps = t.filterApps.toMutableSet(), panel = "", deviceName = "",
                        meshAuto = "", exitVia = mutableListOf())
                }
                ?: newProfile(po.optString("name")).copy(id = id)
            p.name = po.optString("name", p.name)
            p.flag = po.optString("flag", "")
            p.host = po.optString("host", p.host)
            p.sshPort = po.optInt("sshPort", 22)
            p.user = po.optString("user", p.user)
            // Go опускает пустые и false поля (omitempty): нет поля — пусто.
            p.meshEnabled = po.optBoolean("meshEnabled", false)
            p.meshKey = po.optString("meshKey", "")
            p.meshOnlyThis = po.optBoolean("meshOnlyThis", false)
            p.exitVia = po.optJSONArray("exitVia")?.let { a ->
                (0 until a.length()).map { a.optString(it) }.filter { it.isNotBlank() }.toMutableList()
            } ?: mutableListOf()
            p.meshAuto = po.optString("meshAuto", "")
            p.fromPanel = po.optString("fromPanel", "")
            p.panelKey = po.optString("panelKey", "")
            p.hidden = po.optBoolean("hidden", false)
            // У сервера из панели ключ — ключ устройства (у каждого профиля
            // на телефоне свой файл, как и раньше).
            if (p.fromPanel.isNotBlank() && devKey.isNotBlank()) {
                val src = File(devKey)
                val dst = keyFile(p.id)
                if (src.exists() && (!dst.exists() || dst.readText() != src.readText())) {
                    try { saveKeyFor(p.id, src.readText()) } catch (_: Exception) {}
                }
            }
            list.add(p)
        }
        // Пустой список — только если так решил Go (удалены все серверы).
        if (list.isEmpty() && arr.length() > 0) return DeviceChange("", false)
        for (p in old) if (list.none { it.id == p.id }) keyFile(p.id).delete()
        val active = o.optString("active").ifBlank { activeProfileId }
        val activeChanged = active != activeProfileId
        saveAll(list, if (list.any { it.id == active }) active else list.firstOrNull()?.id.orEmpty())
        deviceJson = dev?.toString() ?: ""
        return DeviceChange(o.optString("note"), activeChanged)
    }

    /**
     * Серверы сети, которые программа уже добавляла («id сети/id сервера@адрес»,
     * как Config.MeshAutoSeen на ПК): удалённый или выключенный человеком
     * сервер второй раз не добавляется. JSON-массив для mobile.ApplyNetServers.
     */
    var meshAutoSeenJson: String
        get() = prefs.getString("meshAutoSeen", "[]") ?: "[]"
        private set(v) = prefs.edit().putString("meshAutoSeen", v).apply()

    /** Что поменял ответ mobile.ApplyNetServers — для уведомления. */
    data class NetServersChange(
        val added: List<String>, val off: List<String>, val meshOn: List<String>,
        /** Не сохранённые здесь новые серверы и почему — для журнала (ТЗ 33). */
        val skipped: List<String> = emptyList(),
    )

    /**
     * Сохраняет ответ mobile.ApplyNetServers (ТЗ 26). Решал общий код Go
     * (config.ApplyNetServers) — здесь только перенос в профили телефона:
     * у знакомых профилей — поля сети устройств, новый профиль — копия того,
     * через который о сервере узнали (тот же пользователь и ключ, как на
     * ПК), с адресом и именем нового сервера. Файл ключа копируется: у
     * каждого профиля на телефоне свой.
     */
    fun applyNetServers(json: String): NetServersChange? {
        if (json.isBlank()) return null
        val o = JSONObject(json)
        val arr = o.optJSONArray("profiles") ?: return null
        val list = profiles
        val skipped = mutableListOf<String>()
        val skippedAuto = mutableListOf<String>()
        for (i in 0 until arr.length()) {
            val po = arr.getJSONObject(i)
            val id = po.optString("id")
            if (id.isBlank()) continue
            val known = list.find { it.id == id }
            if (known != null) {
                // Go отдаёт профиль целиком, но пустые и false поля опускает
                // (omitempty): нет поля — значит false / пусто, а не «как
                // было». Раньше «выключить убранный сервер» и «сеть
                // устройств не только через этот» терялись (ТЗ 33).
                known.meshEnabled = po.optBoolean("meshEnabled", false)
                known.meshKey = po.optString("meshKey", "")
                known.meshOnlyThis = po.optBoolean("meshOnlyThis", false)
                known.meshAuto = po.optString("meshAuto", "")
                continue
            }
            // Новый: ищем профиль-образец по его файлу ключа (keyPath в
            // coreProfilesJson — keyFile(id)). Без образца ключа нет — не
            // добавляем: подключиться всё равно было бы нечем.
            val keyPath = po.optString("keyPath")
            val newName = po.optString("name").ifBlank { po.optString("host") }
            val base = list.find { keyFile(it.id).absolutePath == keyPath }
            if (base == null) {
                skipped.add(Lang.wrap(appCtx).getString(R.string.import_skip_no_template, newName, keyPath))
                skippedAuto.add(po.optString("meshAuto"))
                continue
            }
            val p = base.copy(
                id = id,
                name = po.optString("name").ifBlank { po.optString("host") },
                flag = "",
                host = po.optString("host"),
                sshPort = po.optInt("sshPort", 22),
                user = po.optString("user", base.user),
                filterApps = base.filterApps.toMutableSet(),
                panel = "",
                meshEnabled = true,
                meshKey = po.optString("meshKey", base.meshKey),
                meshOnlyThis = false,
                exitVia = mutableListOf(),
                meshAuto = po.optString("meshAuto"),
            )
            val src = keyFile(base.id)
            if (src.exists()) {
                try {
                    saveKeyFor(id, src.readText())
                } catch (e: Exception) {
                    skipped.add(Lang.wrap(appCtx).getString(R.string.import_skip_key_copy, newName, e.message ?: ""))
                    skippedAuto.add(po.optString("meshAuto"))
                    continue
                }
            }
            list.add(p)
        }
        saveAll(list, activeProfileId.ifBlank { list.firstOrNull()?.id.orEmpty() })
        // Не сохранённый здесь сервер не помечаем «уже добавляли»: иначе Go
        // считал бы его удалённым человеком и больше не предлагал.
        o.optJSONArray("seen")?.let { seen ->
            val keep = JSONArray()
            for (i in 0 until seen.length()) {
                val k = seen.optString(i)
                if (skippedAuto.none { it.isNotBlank() && (k == it || k.startsWith("$it@")) }) keep.put(k)
            }
            meshAutoSeenJson = keep.toString()
        }
        fun names(key: String): List<String> = o.optJSONArray(key)?.let { a ->
            (0 until a.length()).map { a.optString(it) }.filter { it.isNotBlank() }
        } ?: emptyList()
        return NetServersChange(names("added"), names("off"), names("meshOn"), skipped)
    }

    fun keyFile(profileId: String): File {
        val dir = File(filesDir, "keys")
        if (!dir.exists()) dir.mkdirs()
        return File(dir, "${profileId}_id_ed25519")
    }

    /**
     * Блокировка рекламы и слежки. Выключена по умолчанию — список пуст, пока
     * человек сам не добавит источник и не нажмёт «Обновить».
     */
    var adBlockEnabled: Boolean
        get() = prefs.getBoolean("adBlockEnabled", false)
        set(v) = prefs.edit().putBoolean("adBlockEnabled", v).apply()

    /** Источники списков: ссылки http(s):// и/или пути к файлам на телефоне. */
    var adBlockSources: String
        get() = prefs.getString("adBlockSources", "") ?: ""
        set(v) = prefs.edit().putString("adBlockSources", v).apply()

    /** Свои исключения — имена, которые никогда не блокируются. */
    var adBlockAllowlist: String
        get() = prefs.getString("adBlockAllowlist", "") ?: ""
        set(v) = prefs.edit().putString("adBlockAllowlist", v).apply()

    /**
     * Сколько раз блокировка сработала за всё время — в отличие от счётчика
     * за сеанс (тот считает ядро и обнуляется при каждом подключении), это
     * значение копится и переживает переподключения и перезапуск приложения.
     */
    var adBlockTotal: Long
        get() = prefs.getLong("adBlockTotal", 0)
        set(v) = prefs.edit().putLong("adBlockTotal", v).apply()

    /**
     * Скачивание обновления в менеджере загрузок, за которым следит экран
     * (-1 — нет). Хранится, чтобы полоска прогресса пережила уход с экрана.
     */
    var updateDownloadId: Long
        get() = prefs.getLong("updateDownloadId", -1)
        set(v) = prefs.edit().putLong("updateDownloadId", v).apply()

    /**
     * Файл со списком заблокированных имён, один на строку. Готовит его
     * UpdateBlockLists (ядро на Go) по нажатию кнопки «Обновить» — и он же
     * читается при каждом подключении, поэтому блокировка работает и без
     * сети, если список уже был загружен хоть раз.
     */
    val adBlockListFile: File get() = File(filesDir, "adblock.list")

    /**
     * Проброс UDP (звонки, игры, QUIC) через ретранслятор на сервере вместо
     * молчаливого отказа. Выключено по умолчанию: пока ретранслятор не
     * установлен на сервере (мастер настройки VPS на компьютере), включать
     * нечего — соединение до него просто не поднимется.
     */
    var udpRelayEnabled: Boolean
        get() = prefs.getBoolean("udpRelayEnabled", false)
        set(v) = prefs.edit().putBoolean("udpRelayEnabled", v).apply()

    /** «Автообновление» по версии сети устройств (ТЗ 40), по умолчанию включено;
     *  autoUpdateT — когда меняли (unix): панель сервера тоже может поменять. */
    var autoUpdate: Boolean
        get() = prefs.getBoolean("autoUpdate", true)
        set(v) = prefs.edit().putBoolean("autoUpdate", v).apply()
    var autoUpdateT: Long
        get() = prefs.getLong("autoUpdateT", 0L)
        set(v) = prefs.edit().putLong("autoUpdateT", v).apply()

    /** Виджет «город и флаг активного сервера» на главном экране — виден
     *  только вместе с этой галочкой и когда серверов больше одного. */
    var showServerPicker: Boolean
        get() = prefs.getBoolean("showServerPicker", false)
        set(v) = prefs.edit().putBoolean("showServerPicker", v).apply()

    val knownHostsFile: File get() = File(filesDir, "known_hosts")

    /**
     * Постоянный id телефона для сети устройств: по нему сервер узнаёт
     * устройство и выдаёт ему тот же адрес. Свой у каждого устройства,
     * поэтому в выгрузку настроек не попадает.
     */
    val deviceId: String
        get() {
            prefs.getString("meshDeviceId", null)?.takeIf { it.isNotBlank() }?.let { return it }
            val id = mobile.Mobile.newDeviceID()
            prefs.edit().putString("meshDeviceId", id).apply()
            return id
        }

    // ---------------------------------------------------------------------
    // Удобные свойства для активного профиля — чтобы TunnelService и старый
    // код экрана, писавшие settings.host/settings.keyFile и т.п., не менялись
    // вовсе: они по-прежнему читают и пишут ровно активный сервер.
    // ---------------------------------------------------------------------

    var host: String
        get() = active().host
        set(v) { val p = active(); p.host = v.trim(); saveProfile(p) }

    var sshPort: Int
        get() = active().sshPort
        set(v) { val p = active(); p.sshPort = v; saveProfile(p) }

    var user: String
        get() = active().user
        set(v) { val p = active(); p.user = v.trim(); saveProfile(p) }

    var poolSize: Int
        get() = active().poolSize
        set(v) { val p = active(); p.poolSize = v; saveProfile(p) }

    var directHosts: String
        get() = active().directHosts
        set(v) { val p = active(); p.directHosts = v; saveProfile(p) }

    var tunnelHosts: String
        get() = active().tunnelHosts
        set(v) { val p = active(); p.tunnelHosts = v; saveProfile(p) }

    var localViaTunnel: Boolean
        get() = active().localViaTunnel
        set(v) { val p = active(); p.localViaTunnel = v; saveProfile(p) }

    var filterMode: String
        get() = active().filterMode
        set(v) { val p = active(); p.filterMode = v; saveProfile(p); shareTraffic(p) }

    var filterApps: Set<String>
        get() = active().filterApps
        set(v) { val p = active(); p.filterApps = v.toMutableSet(); saveProfile(p); shareTraffic(p) }

    val keyFile: File get() = keyFile(active().id)

    val hasKey: Boolean get() = keyFile.exists() && keyFile.length() > 0

    fun saveKey(pem: String) = saveKeyFor(active().id, pem)

    /**
     * Сохраняет закрытый ключ и закрывает его от посторонних.
     *
     * Права важны не для красоты: SSH-клиенты отказываются работать с ключом,
     * который доступен кому-то ещё, и правильно делают.
     */
    fun saveKeyFor(profileId: String, pem: String) {
        val text = pem.trim()
        val f = keyFile(profileId)
        f.writeText(if (text.endsWith("\n")) text else text + "\n")
        applyKeyPermissions(f)
    }

    private fun applyKeyPermissions(f: File) {
        f.setReadable(false, false)
        f.setWritable(false, false)
        f.setReadable(true, true)
        f.setWritable(true, true)
    }

    fun ready(): Boolean = host.isNotBlank() && user.isNotBlank() && hasKey

    // ---------------------------------------------------------------------
    // Язык интерфейса.
    // ---------------------------------------------------------------------

    var language: String
        get() = prefs.getString("language", "ru") ?: "ru"
        set(v) = prefs.edit().putString("language", v).apply()

    // ---------------------------------------------------------------------
    // Оформление и запуск — как в «Настройках приложения» на компьютере.
    // ---------------------------------------------------------------------

    /** "" — оригинал, "dark", "light", "console". */
    var theme: String
        get() = prefs.getString("theme", "") ?: ""
        set(v) = prefs.edit().putString("theme", v).apply()

    /** Фон главного экрана: "" — ровный цвет, "code", "author". */
    var background: String
        get() = prefs.getString("background", "") ?: ""
        set(v) = prefs.edit().putString("background", v).apply()

    /**
     * Туннель при открытии приложения: "on" — включать, "last" — как было
     * после последнего включения/выключения руками (lastRunning), "off" — нет.
     * По умолчанию "off": раньше приложение само не подключалось.
     */
    var startMode: String
        get() = prefs.getString("startMode", "off") ?: "off"
        set(v) = prefs.edit().putString("startMode", v).apply()

    /** Был ли включён обход блокировок при последнем действии руками. */
    var lastRunning: Boolean
        get() = prefs.getBoolean("lastRunning", false)
        set(v) = prefs.edit().putBoolean("lastRunning", v).apply()

    /**
     * Сеть устройств при открытии приложения — отдельно от туннеля, те же
     * "on", "last" (lastMesh), "off". Пусто — повторяет выбор туннеля, как
     * было до появления настройки.
     */
    var meshStartMode: String
        get() = prefs.getString("meshStartMode", "") ?: ""
        set(v) = prefs.edit().putString("meshStartMode", v).apply()

    var lastMesh: Boolean
        get() = prefs.getBoolean("lastMesh", false)
        set(v) = prefs.edit().putBoolean("lastMesh", v).apply()

    /** Действующий выбор для сети: пустой повторяет туннель. */
    val meshStartEffective: String get() = meshStartMode.ifBlank { startMode }

    /** Перенос настроек и списков: "clip" — буфер обмена, "file" — файлом. */
    var xferMode: String
        get() = prefs.getString("xferMode", "clip") ?: "clip"
        set(v) = prefs.edit().putString("xferMode", v).apply()

    /** У активного сервера есть сеть устройств. */
    fun meshConfigured(): Boolean = active().let { it.meshEnabled && it.meshKey.isNotBlank() }

}
