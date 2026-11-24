package io.github.vitazgio.sshtunnel

import android.content.Context
import android.content.res.Configuration
import java.util.Locale

/**
 * Язык приложения (ТЗ 37). Окно берёт его из настройки «Язык» (Settings.language),
 * и всё, что рисует сам Android — уведомления, плитка, виджет, тосты, журнал, —
 * должно говорить на том же языке, а не на языке системы. [wrap] возвращает
 * контекст, чьи ресурсы (res/values, res/values-en) выбраны по этой настройке.
 */
object Lang {
    /** "en" или "ru" — как в окне (AndroidApi: параметр lang). */
    fun code(c: Context): String {
        val v = try {
            c.getSharedPreferences("settings", Context.MODE_PRIVATE).getString("language", "ru")
        } catch (_: Exception) {
            "ru"
        }
        return if (v == "en") "en" else "ru"
    }

    fun locale(c: Context): Locale = Locale(code(c))

    fun wrap(base: Context): Context {
        val cfg = Configuration(base.resources.configuration)
        cfg.setLocale(locale(base))
        return base.createConfigurationContext(cfg)
    }
}
