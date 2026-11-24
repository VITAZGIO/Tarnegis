package io.github.vitazgio.sshtunnel

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.res.Configuration
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.util.SizeF
import android.util.TypedValue
import android.view.View
import android.widget.RemoteViews
import org.json.JSONArray
import java.time.DayOfWeek
import java.time.LocalDate
import java.time.YearMonth
import java.time.ZoneId
import java.time.ZoneOffset
import java.time.format.TextStyle
import java.time.temporal.IsoFields
import java.util.Calendar
import java.util.Locale

// Месячный виджет календаря (ТЗ 41): сетка из шести недель, события полосками
// по высоте клетки (в самом малом размере — точки), листание месяцев, «сегодня»,
// «+» (новое событие), «↻» (сверка), номера недель по желанию. Данные — из ядра
// (mobile.CalendarRangeJSON), тот же источник, что у виджета «Ближайшие».
// Листание помнится на экземпляр виджета до смены дня. Язык — приложения (Lang).
class CalendarMonthWidget : AppWidgetProvider() {

    override fun onUpdate(ctx: Context, mgr: AppWidgetManager, ids: IntArray) {
        CalendarSync.init(ctx)
        ids.forEach { redraw(ctx, mgr, it) }
    }

    // Размер изменили растяжкой: пересчитать, сколько полосок помещается.
    override fun onAppWidgetOptionsChanged(ctx: Context, mgr: AppWidgetManager, id: Int, opts: Bundle) {
        redraw(ctx, mgr, id)
    }

    override fun onDeleted(ctx: Context, ids: IntArray) {
        val e = prefs(ctx).edit()
        ids.forEach { e.remove("off_$it").remove("day_$it").remove("weeks_$it") }
        e.apply()
    }

    override fun onReceive(ctx: Context, intent: Intent) {
        val id = intent.getIntExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, AppWidgetManager.INVALID_APPWIDGET_ID)
        when (intent.action) {
            ACT_PREV -> shift(ctx, id, -1)
            ACT_NEXT -> shift(ctx, id, +1)
            ACT_TODAY -> setOffset(ctx, id, 0)
            ACT_WEEKS -> prefs(ctx).edit().putBoolean("weeks_$id", !prefs(ctx).getBoolean("weeks_$id", false)).apply()
            ACT_SYNC -> CalendarSync.requestSync(ctx)
            // Сменился день, время или пояс: «сегодня» и сброс листания.
            Intent.ACTION_DATE_CHANGED, Intent.ACTION_TIME_CHANGED, Intent.ACTION_TIMEZONE_CHANGED -> {
                CalendarSync.init(ctx)
                updateAll(ctx)
                return
            }
            else -> {
                super.onReceive(ctx, intent)
                return
            }
        }
        if (id != AppWidgetManager.INVALID_APPWIDGET_ID) {
            CalendarSync.init(ctx)
            redraw(ctx, AppWidgetManager.getInstance(ctx), id)
        }
    }

    companion object {
        private const val PREFS = "calendar_month_widget"
        private const val ACT_PREV = "io.github.vitazgio.sshtunnel.CAL_MONTH_PREV"
        private const val ACT_NEXT = "io.github.vitazgio.sshtunnel.CAL_MONTH_NEXT"
        private const val ACT_TODAY = "io.github.vitazgio.sshtunnel.CAL_MONTH_TODAY"
        private const val ACT_WEEKS = "io.github.vitazgio.sshtunnel.CAL_MONTH_WEEKS"
        private const val ACT_SYNC = "io.github.vitazgio.sshtunnel.CAL_MONTH_SYNC"

        // Размеры в dp: должны совпадать с res/layout/widget_calendar_month*.xml.
        private const val PAD = 8f          // поля виджета
        private const val HEADER = 34f      // шапка
        private const val DOW = 16f         // строка дней недели
        private const val WEEKCOL = 16f     // колонка номеров недель
        private const val NUM_H = 15f       // строка с числом в клетке
        private const val BAR_H = 12f       // полоска события (11 + отступ 1)
        private const val MAX_BARS = 5
        private const val MIN_BAR_CELL_W = 40f
        private const val MIN_WIDE_W = 300f // уже — без кнопки «№», мельче заголовок

        private val DOW_IDS = intArrayOf(R.id.mD0, R.id.mD1, R.id.mD2, R.id.mD3, R.id.mD4, R.id.mD5, R.id.mD6)
        private val BAR_IDS = intArrayOf(R.id.cE0, R.id.cE1, R.id.cE2, R.id.cE3, R.id.cE4)
        private val DOT_IDS = intArrayOf(R.id.cDot0, R.id.cDot1, R.id.cDot2)

        fun updateAll(ctx: Context) {
            val mgr = AppWidgetManager.getInstance(ctx)
            mgr.getAppWidgetIds(ComponentName(ctx, CalendarMonthWidget::class.java))
                .forEach { redraw(ctx, mgr, it) }
        }

        private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

        // Листание помнится до смены дня, потом снова текущий месяц.
        private fun offset(ctx: Context, id: Int): Int {
            val p = prefs(ctx)
            return if (p.getLong("day_$id", -1) == LocalDate.now().toEpochDay()) p.getInt("off_$id", 0) else 0
        }

        private fun setOffset(ctx: Context, id: Int, off: Int) {
            prefs(ctx).edit().putInt("off_$id", off).putLong("day_$id", LocalDate.now().toEpochDay()).apply()
        }

        private fun shift(ctx: Context, id: Int, d: Int) = setOffset(ctx, id, offset(ctx, id) + d)

        private fun firstDow(ctx: Context): DayOfWeek {
            if (Lang.code(ctx) == "ru") return DayOfWeek.MONDAY
            // Английский: как принято в локали телефона (Calendar: 1 = вс … 7 = сб).
            val c = Calendar.getInstance(Locale.getDefault()).firstDayOfWeek
            return DayOfWeek.of((c + 5) % 7 + 1)
        }

        private fun redraw(ctx: Context, mgr: AppWidgetManager, id: Int) {
            val o = mgr.getAppWidgetOptions(id)
            var views: RemoteViews? = null
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                // Android 12+: лаунчер сам выбирает вёрстку под текущий размер
                // (поворот без перерисовки).
                val sizes = o.getParcelableArrayList<SizeF>(AppWidgetManager.OPTION_APPWIDGET_SIZES)
                if (!sizes.isNullOrEmpty()) {
                    views = RemoteViews(sizes.associateWith { build(ctx, id, it.width, it.height) })
                }
            }
            if (views == null) {
                val land = ctx.resources.configuration.orientation == Configuration.ORIENTATION_LANDSCAPE
                var w = o.getInt(if (land) AppWidgetManager.OPTION_APPWIDGET_MAX_WIDTH else AppWidgetManager.OPTION_APPWIDGET_MIN_WIDTH, 0).toFloat()
                var h = o.getInt(if (land) AppWidgetManager.OPTION_APPWIDGET_MIN_HEIGHT else AppWidgetManager.OPTION_APPWIDGET_MAX_HEIGHT, 0).toFloat()
                if (w <= 0f) w = 250f
                if (h <= 0f) h = 250f
                views = build(ctx, id, w, h)
            }
            mgr.updateAppWidget(id, views)
        }

        private class Ev(val title: String, val color: Int, val start: Long, val allDay: Boolean,
                         val href: String, val order: Long)

        // Цвет календаря из ядра: «#RRGGBB», бывает «#RRGGBBAA» (Nextcloud) — альфу отбрасываем.
        private fun parseColor(ctx: Context, s: String): Int {
            try {
                var t = s.trim()
                if (t.length == 9 && t.startsWith("#")) t = t.substring(0, 7)
                if (t.isNotEmpty()) return Color.parseColor(t)
            } catch (_: Exception) {
            }
            return ctx.getColor(R.color.accent)
        }

        // Текст на полоске: тёмный на светлом цвете, белый на тёмном.
        private fun onColor(c: Int): Int {
            val l = (0.299 * Color.red(c) + 0.587 * Color.green(c) + 0.114 * Color.blue(c)) / 255.0
            return if (l > 0.62) 0xFF0D1017.toInt() else Color.WHITE
        }

        // События 42 дней сетки, разложенные по клеткам (многодневные — на каждый день).
        private fun load(ctx: Context, start: LocalDate): Array<MutableList<Ev>> {
            val days = Array(42) { mutableListOf<Ev>() }
            val zone = ZoneId.systemDefault()
            val from = start.atStartOfDay(zone).toInstant().toEpochMilli()
            val to = start.plusDays(42).atStartOfDay(zone).toInstant().toEpochMilli()
            val items = try {
                JSONArray(mobile.Mobile.calendarRangeJSON(from, to))
            } catch (_: Exception) {
                JSONArray()
            }
            for (i in 0 until items.length()) {
                val e = items.getJSONObject(i)
                val s = e.getLong("start")
                val en = e.getLong("end")
                val allDay = e.optBoolean("allDay")
                // Событие на весь день — полночь UTC: даты берём по UTC, конец не входит.
                val zs = if (allDay) ZoneOffset.UTC else zone
                var d0 = java.time.Instant.ofEpochMilli(s).atZone(zs).toLocalDate()
                var d1 = if (allDay) java.time.Instant.ofEpochMilli(en).atZone(zs).toLocalDate().minusDays(1)
                else java.time.Instant.ofEpochMilli(if (en > s) en - 1 else s).atZone(zs).toLocalDate()
                if (d1.isBefore(d0)) d1 = d0
                val ev = Ev(e.optString("title"), parseColor(ctx, e.optString("color")), s, allDay,
                    e.optString("href"), (if (allDay) 0L else 1L) * Long.MAX_VALUE / 2 + s / 1000)
                if (d0.isBefore(start)) d0 = start
                val last = start.plusDays(41)
                if (d1.isAfter(last)) d1 = last
                var d = d0
                while (!d.isAfter(d1)) {
                    days[(d.toEpochDay() - start.toEpochDay()).toInt()].add(ev)
                    d = d.plusDays(1)
                }
            }
            days.forEach { l -> l.sortBy { it.order } }
            return days
        }

        private fun broadcast(ctx: Context, id: Int, act: String): PendingIntent {
            val i = Intent(ctx, CalendarMonthWidget::class.java).setAction(act)
                .putExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, id)
                .setData(Uri.parse("sshtunnel://calmonth/$id/$act"))
            return PendingIntent.getBroadcast(ctx, id, i, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        }

        // Календарь приложения: на дне, на новом событии или на этом событии.
        // Адрес со свежим ключом собирает WebPageActivity при запуске.
        private fun open(ctx: Context, key: String, day: LocalDate?, event: String?, isNew: Boolean): PendingIntent {
            val i = Intent(ctx, WebPageActivity::class.java)
                .putExtra(WebPageActivity.EXTRA_PAGE, WebPageActivity.PAGE_APPS)
                .putExtra(WebPageActivity.EXTRA_OPEN, "calendar")
                .setData(Uri.parse("sshtunnel://calmonth/open/$key"))
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            if (day != null) i.putExtra(WebPageActivity.EXTRA_DAY, day.toString())
            if (event != null) i.putExtra(WebPageActivity.EXTRA_EVENT, event)
            if (isNew) i.putExtra(WebPageActivity.EXTRA_NEW, "1")
            return PendingIntent.getActivity(ctx, key.hashCode(), i, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        }

        private fun build(ctx: Context, id: Int, wDp: Float, hDp: Float): RemoteViews {
            val t = Lang.wrap(ctx)
            val today = LocalDate.now()
            val month = YearMonth.from(today).plusMonths(offset(ctx, id).toLong())
            val fd = firstDow(ctx)
            val first = month.atDay(1)
            val start = first.minusDays(((first.dayOfWeek.value - fd.value + 7) % 7).toLong())
            val weeks = prefs(ctx).getBoolean("weeks_$id", false)
            val loc = Lang.locale(ctx)
            val days = load(ctx, start)

            // Что помещается: клетка = (высота − поля, шапка, дни недели) / 6.
            val gridH = hDp - 2 * PAD - HEADER - DOW
            val cellH = gridH / 6f
            val cellW = (wDp - 2 * PAD - (if (weeks) WEEKCOL else 0f)) / 7f
            val slots = ((cellH - NUM_H) / BAR_H).toInt().coerceIn(0, MAX_BARS)
            val bars = slots >= 2 && cellW >= MIN_BAR_CELL_W

            val rv = RemoteViews(ctx.packageName, R.layout.widget_calendar_month)
            val title = month.month.getDisplayName(TextStyle.FULL_STANDALONE, loc)
                .replaceFirstChar { it.titlecase(loc) } + " " + month.year
            rv.setTextViewText(R.id.mTitle, title)
            val wide = wDp >= MIN_WIDE_W
            rv.setTextViewTextSize(R.id.mTitle, TypedValue.COMPLEX_UNIT_SP, if (wide) 16f else 14f)
            rv.setViewVisibility(R.id.mWeeks, if (wide) View.VISIBLE else View.GONE)
            rv.setTextColor(R.id.mWeeks, ctx.getColor(if (weeks) R.color.accent else R.color.dim))

            rv.setOnClickPendingIntent(R.id.mTitle, broadcast(ctx, id, ACT_TODAY))
            rv.setOnClickPendingIntent(R.id.mPrev, broadcast(ctx, id, ACT_PREV))
            rv.setOnClickPendingIntent(R.id.mNext, broadcast(ctx, id, ACT_NEXT))
            rv.setOnClickPendingIntent(R.id.mWeeks, broadcast(ctx, id, ACT_WEEKS))
            rv.setOnClickPendingIntent(R.id.mSync, broadcast(ctx, id, ACT_SYNC))
            rv.setOnClickPendingIntent(R.id.mAdd, open(ctx, "new/$today", today, null, true))
            rv.setContentDescription(R.id.mTitle, t.getString(R.string.cal_cd_today))
            rv.setContentDescription(R.id.mPrev, t.getString(R.string.cal_cd_prev))
            rv.setContentDescription(R.id.mNext, t.getString(R.string.cal_cd_next))
            rv.setContentDescription(R.id.mAdd, t.getString(R.string.cal_cd_add))
            rv.setContentDescription(R.id.mSync, t.getString(R.string.cal_cd_sync))
            rv.setContentDescription(R.id.mWeeks, t.getString(R.string.cal_cd_weeks))

            // Дни недели в порядке первого дня.
            val names = arrayOf(R.string.dow_1, R.string.dow_2, R.string.dow_3, R.string.dow_4, R.string.dow_5, R.string.dow_6, R.string.dow_7)
            for (c in 0 until 7) rv.setTextViewText(DOW_IDS[c], t.getString(names[(fd.value - 1 + c) % 7]))
            rv.setViewVisibility(R.id.mDowPad, if (weeks) View.VISIBLE else View.GONE)

            val dim = (ctx.getColor(R.color.dim) and 0x00FFFFFF) or 0x80000000.toInt()
            for (w in 0 until 6) {
                val wk = RemoteViews(ctx.packageName, R.layout.widget_calendar_month_week)
                if (weeks) {
                    val thursday = start.plusDays((w * 7 + (DayOfWeek.THURSDAY.value - fd.value + 7) % 7).toLong())
                    wk.setViewVisibility(R.id.wkNum, View.VISIBLE)
                    wk.setTextViewText(R.id.wkNum, thursday.get(IsoFields.WEEK_OF_WEEK_BASED_YEAR).toString())
                }
                for (c in 0 until 7) {
                    val day = start.plusDays((w * 7 + c).toLong())
                    val evs = days[w * 7 + c]
                    wk.addView(R.id.wkCells, cell(ctx, t, id, day, month, today, evs, bars, slots, dim))
                }
                rv.addView(R.id.mGrid, wk)
            }
            return rv
        }

        private fun cell(ctx: Context, t: Context, id: Int, day: LocalDate, month: YearMonth, today: LocalDate,
                         evs: List<Ev>, bars: Boolean, slots: Int, dim: Int): RemoteViews {
            val rv = RemoteViews(ctx.packageName, R.layout.widget_calendar_month_cell)
            rv.setTextViewText(R.id.cNum, day.dayOfMonth.toString())
            val inMonth = YearMonth.from(day) == month
            when {
                day == today -> {
                    rv.setInt(R.id.cNum, "setBackgroundResource", R.drawable.bg_month_today)
                    rv.setTextColor(R.id.cNum, Color.WHITE)
                }
                inMonth -> rv.setTextColor(R.id.cNum, ctx.getColor(R.color.text))
                else -> rv.setTextColor(R.id.cNum, dim)
            }
            rv.setOnClickPendingIntent(R.id.cRoot, open(ctx, "d/$id/$day", day, null, false))
            if (evs.isEmpty()) return rv
            if (!bars) {
                // Самый малый размер: до трёх точек разных цветов под числом.
                rv.setViewVisibility(R.id.cDots, View.VISIBLE)
                val colors = evs.map { it.color }.distinct().take(DOT_IDS.size)
                colors.forEachIndexed { i, c ->
                    rv.setViewVisibility(DOT_IDS[i], View.VISIBLE)
                    rv.setInt(DOT_IDS[i], "setColorFilter", c)
                }
                return rv
            }
            // Полоски: влезает slots строк; не влезло — последняя строка «+N».
            val shown = if (evs.size <= slots) evs.size else slots - 1
            for (i in 0 until shown) {
                val e = evs[i]
                rv.setViewVisibility(BAR_IDS[i], View.VISIBLE)
                rv.setTextViewText(BAR_IDS[i], e.title)
                rv.setInt(BAR_IDS[i], "setBackgroundColor", e.color)
                rv.setTextColor(BAR_IDS[i], onColor(e.color))
                // Нажатие на полоску открывает событие; ключ — адрес и начало (мс).
                val ref = if (e.href.isNotEmpty()) e.href + "|" + e.start else null
                rv.setOnClickPendingIntent(BAR_IDS[i], open(ctx, "e/$id/$day/$i", day, ref, false))
            }
            if (evs.size > shown) {
                rv.setViewVisibility(R.id.cMore, View.VISIBLE)
                rv.setTextViewText(R.id.cMore, t.getString(R.string.cal_more, evs.size - shown))
            }
            return rv
        }
    }
}
