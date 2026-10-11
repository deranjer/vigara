package org.vigara.companion

import android.content.ContentResolver
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import android.net.Uri
import android.provider.OpenableColumns
import java.util.UUID

data class SelectedFile(val id: String, val uri: String, val name: String, val size: Long, val state: String)
data class PendingFile(val selected: SelectedFile, val lastHash: String, val pendingHash: String, val pendingBackupId: String)

class SelectedFiles(context: Context) : SQLiteOpenHelper(context, "selected-files.db", null, 1) {
    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL("CREATE TABLE selected_files(id TEXT PRIMARY KEY, uri TEXT UNIQUE NOT NULL, name TEXT NOT NULL, size INTEGER NOT NULL, state TEXT NOT NULL, last_hash TEXT NOT NULL DEFAULT '', pending_hash TEXT NOT NULL DEFAULT '', pending_backup_id TEXT NOT NULL DEFAULT '')")
    }
    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) = Unit

    fun add(resolver: ContentResolver, uri: Uri, grantFlags: Int): Boolean {
        val readFlag = grantFlags and android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION
        if (readFlag == 0) return false
        try { resolver.takePersistableUriPermission(uri, readFlag) } catch (_: SecurityException) { return false }
        val (name, size) = metadata(resolver, uri) ?: run {
            try { resolver.releasePersistableUriPermission(uri, readFlag) } catch (_: SecurityException) { }
            return false
        }
        val safeName = safeDisplayName(name) ?: return false
        val values = android.content.ContentValues().apply {
            put("id", UUID.randomUUID().toString()); put("uri", uri.toString()); put("name", safeName); put("size", size); put("state", "Ready")
        }
        writableDatabase.insertWithOnConflict("selected_files", null, values, SQLiteDatabase.CONFLICT_IGNORE)
        return true
    }

    fun all(): List<SelectedFile> = readableDatabase.rawQuery("SELECT id,uri,name,size,state FROM selected_files ORDER BY name", null).use { cursor ->
        buildList { while (cursor.moveToNext()) add(SelectedFile(cursor.getString(0), cursor.getString(1), cursor.getString(2), cursor.getLong(3), cursor.getString(4))) }
    }

    fun pending(): List<PendingFile> = readableDatabase.rawQuery("SELECT id,uri,name,size,state,last_hash,pending_hash,pending_backup_id FROM selected_files ORDER BY name", null).use { cursor ->
        buildList { while (cursor.moveToNext()) {
            val selected = SelectedFile(cursor.getString(0), cursor.getString(1), cursor.getString(2), cursor.getLong(3), cursor.getString(4))
            add(PendingFile(selected, cursor.getString(5), cursor.getString(6), cursor.getString(7)))
        } }
    }

    fun pendingVersion(id: String, hash: String, backupId: String) = writableDatabase.execSQL("UPDATE selected_files SET pending_hash=?,pending_backup_id=?,state='Waiting to upload' WHERE id=?", arrayOf(hash, backupId, id))
    fun state(id: String, state: String) = writableDatabase.execSQL("UPDATE selected_files SET state=? WHERE id=?", arrayOf(state, id))
    fun uploaded(id: String, hash: String) = writableDatabase.execSQL("UPDATE selected_files SET last_hash=?,pending_hash='',pending_backup_id='',state='Backed up' WHERE id=?", arrayOf(hash, id))

    fun remove(id: String) { writableDatabase.delete("selected_files", "id=?", arrayOf(id)) }

    fun metadata(resolver: ContentResolver, uri: Uri): Pair<String, Long>? = try {
        resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
            if (!c.moveToFirst() || c.isNull(1)) return null
            val name = c.getString(0) ?: return null
            val size = c.getLong(1)
            if (size <= 0 || size > MAX_FILE_BYTES) return null
            name to size
        }
    } catch (_: Exception) { null }

    companion object {
        const val MAX_FILE_BYTES = 512L * 1024L * 1024L
        fun safeDisplayName(value: String): String? {
            val name = value.substringAfterLast('/').substringAfterLast('\\')
            if (name.isBlank() || name == "." || name == ".." || name.length > 200 || name.any { it.isISOControl() }) return null
            return name
        }
    }
}
