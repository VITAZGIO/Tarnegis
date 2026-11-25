package io.github.vitazgio.sshtunnel

import android.Manifest
import android.app.Activity
import android.content.ContentUris
import android.content.Context
import android.content.pm.PackageManager
import android.database.ContentObserver
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.provider.MediaStore
import android.widget.Toast

/**
 * ТЗ 52: «Скриншоты сразу в общий буфер». Сделал скриншот на телефоне — на ПК
 * он уже в буфере картинкой, Ctrl+V в чат.
 *
 * Новую картинку замечает ContentObserver на MediaStore (он работает и в
 * фоне, пока жив процесс: служба VPN или общего буфера). ScreenCaptureCallback
 * Android 14 не годится — он только про свою activity. Что отправлять (папка
 * скриншотов, свежесть, без повторов) решает ядро: mobile.ScreenshotWanted.
 * Галочка (по умолчанию выключена) — в ядре, POST /api/clipboard/settings.
 */
object Screenshots {
    @Volatile private var app: Context? = null
    private val main = Handler(Looper.getMainLooper())
    private val worker: Handler by lazy {
        Handler(HandlerThread("screenshots").apply { start() }.looper)
    }
    private val scan = Runnable { scanNow() }

    fun watch(ctx: Context) {
        if (app != null) return
        val c = ctx.applicationContext
        app = c
        try {
            c.contentResolver.registerContentObserver(
                MediaStore.Images.Media.EXTERNAL_CONTENT_URI, true,
                object : ContentObserver(worker) {
                    override fun onChange(selfChange: Boolean, uri: Uri?) {
                        // Пачка уведомлений на один скриншот — разбираемся, когда утихнет.
                        worker.removeCallbacks(scan)
                        worker.postDelayed(scan, 600)
                    }
                })
        } catch (_: Exception) {
        }
    }

    /** Разрешение, которое нужно: Android 13+ — «Фото и видео», раньше — память. */
    private fun permission(): String =
        if (Build.VERSION.SDK_INT >= 33) Manifest.permission.READ_MEDIA_IMAGES
        else Manifest.permission.READ_EXTERNAL_STORAGE

    /** Есть доступ к новым картинкам. «Только выбранные фото» (Android 14) не
     * годится: новых скриншотов в нём нет. «Доступ ко всем файлам» — годится. */
    fun permitted(c: Context): Boolean {
        if (Build.VERSION.SDK_INT >= 30 && Environment.isExternalStorageManager()) return true
        return c.checkSelfPermission(permission()) == PackageManager.PERMISSION_GRANTED
    }

    /** Спросить доступ (страница «Общий буфер»). */
    fun ask(a: Activity) {
        if (permitted(a)) return
        try {
            a.requestPermissions(arrayOf(permission()), REQ)
        } catch (_: Exception) {
        }
    }

    const val REQ = 5201

    private fun scanNow() {
        val c = app ?: return
        if (!(try { mobile.Mobile.screenshotsOn() } catch (_: Throwable) { false })) return
        if (!permitted(c)) return
        val q = Build.VERSION.SDK_INT >= 29
        val cols = if (q) arrayOf(
            MediaStore.Images.Media._ID, MediaStore.Images.Media.RELATIVE_PATH,
            MediaStore.Images.Media.DISPLAY_NAME, MediaStore.Images.Media.DATE_ADDED,
            MediaStore.Images.Media.IS_PENDING,
        ) else arrayOf(
            MediaStore.Images.Media._ID, MediaStore.Images.Media.DATA,
            MediaStore.Images.Media.DISPLAY_NAME, MediaStore.Images.Media.DATE_ADDED,
        )
        val since = System.currentTimeMillis() / 1000 - 120
        val found = ArrayList<Pair<Uri, String>>()
        try {
            c.contentResolver.query(
                MediaStore.Images.Media.EXTERNAL_CONTENT_URI, cols,
                "${MediaStore.Images.Media.DATE_ADDED} >= ?", arrayOf(since.toString()),
                "${MediaStore.Images.Media.DATE_ADDED} DESC",
            )?.use { cur ->
                var n = 0
                while (cur.moveToNext() && n++ < 10) {
                    if (q && cur.getInt(4) != 0) continue // ещё пишется — придёт новое уведомление
                    val id = cur.getLong(0)
                    val path = cur.getString(1) ?: ""
                    val name = cur.getString(2) ?: ""
                    val added = cur.getLong(3)
                    if (mobile.Mobile.screenshotWanted(id, path, name, added)) {
                        found.add(ContentUris.withAppendedId(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, id) to name)
                    }
                }
            }
        } catch (_: Exception) {
            return
        }
        for ((uri, name) in found.asReversed()) send(c, uri, name)
    }

    private fun send(c: Context, uri: Uri, name: String) {
        val raw = try {
            c.contentResolver.openInputStream(uri)?.use { it.readBytes() }
        } catch (_: Throwable) {
            null
        } ?: return
        val png = toPng(raw, mobile.Mobile.screenshotLimit()) ?: return
        val base = name.substringBeforeLast('.', name).ifEmpty { "screenshot" }
        val msg = Tools5.push(c, "image", "$base.png", "image/png", png)
        main.post { Toast.makeText(c, msg, Toast.LENGTH_SHORT).show() }
    }

    /** Картинка → PNG не больше limit байт (ПК кладёт в буфер именно PNG).
     * PNG в пределах — как есть; иначе перекодировать, уменьшая вдвое, пока
     * не влезет. */
    private fun toPng(data: ByteArray, limit: Long): ByteArray? {
        val isPng = data.size >= 4 && data[0] == 0x89.toByte() && data[1] == 0x50.toByte() &&
            data[2] == 0x4E.toByte() && data[3] == 0x47.toByte()
        if (isPng && data.size <= limit) return data
        return try {
            var bmp: Bitmap = BitmapFactory.decodeByteArray(data, 0, data.size) ?: return null
            var out: ByteArray
            while (true) {
                val buf = java.io.ByteArrayOutputStream()
                bmp.compress(Bitmap.CompressFormat.PNG, 100, buf)
                out = buf.toByteArray()
                if (out.size <= limit || bmp.width < 320) break
                val next = Bitmap.createScaledBitmap(bmp, bmp.width / 2, bmp.height / 2, true)
                bmp.recycle()
                bmp = next
            }
            bmp.recycle()
            out
        } catch (_: Throwable) {
            null
        }
    }
}
