package org.vigara.companion

import android.content.ContentResolver
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import android.net.Uri
import android.provider.OpenableColumns
import java.util.UUID
import java.io.InputStream
import java.io.IOException
import java.security.MessageDigest

data class SelectedFile(val id: String, val uri: String, val name: String, val size: Long, val state: String)
data class PendingFile(val selected: SelectedFile, val lastHash: String, val pendingHash: String, val pendingBackupId: String, val generation: String)

class SelectedFiles(context: Context) : SQLiteOpenHelper(context, "selected-files.db", null, 2) {
    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL("CREATE TABLE selected_files(id TEXT PRIMARY KEY, uri TEXT UNIQUE NOT NULL, name TEXT NOT NULL, size INTEGER NOT NULL, state TEXT NOT NULL, last_hash TEXT NOT NULL DEFAULT '', pending_hash TEXT NOT NULL DEFAULT '', pending_backup_id TEXT NOT NULL DEFAULT '', generation TEXT NOT NULL DEFAULT '')")
    }
    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
        if (oldVersion < 2) db.execSQL("ALTER TABLE selected_files ADD COLUMN generation TEXT NOT NULL DEFAULT ''")
    }

    fun add(resolver: ContentResolver, uri: Uri, grantFlags: Int, generation: String): Boolean = add(
        uri = uri,
        grantFlags = grantFlags,
        generation = generation,
        takePermission = resolver::takePersistableUriPermission,
        releasePermission = resolver::releasePersistableUriPermission,
        metadata = { metadata(resolver, it) },
    )

    internal fun add(
        uri: Uri,
        grantFlags: Int,
        generation: String,
        takePermission: (Uri, Int) -> Unit,
        releasePermission: (Uri, Int) -> Unit,
        metadata: (Uri) -> Pair<String, Long>?,
    ): Boolean {
        val readFlag = grantFlags and android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION
        if (readFlag == 0) return false
        try { takePermission(uri, readFlag) } catch (_: SecurityException) { return false }
        val (name, size) = metadata(uri) ?: run {
            try { releasePermission(uri, readFlag) } catch (_: SecurityException) { }
            return false
        }
        if (!supportedSize(size)) {
            try { releasePermission(uri, readFlag) } catch (_: SecurityException) { }
            return false
        }
        val safeName = safeDisplayName(name) ?: run {
            try { releasePermission(uri, readFlag) } catch (_: SecurityException) { }
            return false
        }
        val values = android.content.ContentValues().apply {
            put("id", UUID.randomUUID().toString()); put("uri", uri.toString()); put("name", safeName); put("size", size); put("state", "Ready"); put("generation", generation)
        }
        writableDatabase.insertWithOnConflict("selected_files", null, values, SQLiteDatabase.CONFLICT_IGNORE)
        return true
    }

    fun all(): List<SelectedFile> = readableDatabase.rawQuery("SELECT id,uri,name,size,state FROM selected_files ORDER BY name", null).use { cursor ->
        buildList { while (cursor.moveToNext()) add(SelectedFile(cursor.getString(0), cursor.getString(1), cursor.getString(2), cursor.getLong(3), cursor.getString(4))) }
    }

    fun switchGeneration(generation: String) {
        writableDatabase.execSQL("UPDATE selected_files SET generation=?,last_hash='',pending_hash='',pending_backup_id='',state='Ready' WHERE generation<>?", arrayOf(generation, generation))
    }

    fun pending(generation: String): List<PendingFile> = readableDatabase.rawQuery("SELECT id,uri,name,size,state,last_hash,pending_hash,pending_backup_id,generation FROM selected_files WHERE generation=? ORDER BY name", arrayOf(generation)).use { cursor ->
        buildList { while (cursor.moveToNext()) {
            val selected = SelectedFile(cursor.getString(0), cursor.getString(1), cursor.getString(2), cursor.getLong(3), cursor.getString(4))
            add(PendingFile(selected, cursor.getString(5), cursor.getString(6), cursor.getString(7), cursor.getString(8)))
        } }
    }

    fun pendingVersion(id: String, generation: String, hash: String, backupId: String) = writableDatabase.execSQL("UPDATE selected_files SET pending_hash=?,pending_backup_id=?,state='Waiting to upload' WHERE id=? AND generation=?", arrayOf(hash, backupId, id, generation))
    fun state(id: String, generation: String, state: String) = writableDatabase.execSQL("UPDATE selected_files SET state=? WHERE id=? AND generation=?", arrayOf(state, id, generation))
    fun uploaded(id: String, generation: String, hash: String) = writableDatabase.execSQL("UPDATE selected_files SET last_hash=?,pending_hash='',pending_backup_id='',state='Backed up' WHERE id=? AND generation=?", arrayOf(hash, id, generation))

    fun remove(id: String) { writableDatabase.delete("selected_files", "id=?", arrayOf(id)) }

    fun metadata(resolver: ContentResolver, uri: Uri): Pair<String, Long>? = try {
        resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
            if (!c.moveToFirst() || c.isNull(1)) return null
            val name = c.getString(0) ?: return null
            val size = c.getLong(1)
            if (size < 0) return null
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
        fun supportedSize(size: Long): Boolean = size in 1..MAX_FILE_BYTES

        fun refreshMetadata(file: SelectedFile, name: String, size: Long): SelectedFile? {
            if (!supportedSize(size)) return null
            val safeName = safeDisplayName(name) ?: return null
            return file.copy(name = safeName, size = size)
        }

        fun digestSized(input: InputStream, expectedSize: Long): String {
            if (!supportedSize(expectedSize)) throw IOException("Unsupported file size")
            val digest = MessageDigest.getInstance("SHA-256")
            input.use { source ->
                val buffer = ByteArray(64 * 1024)
                var total = 0L
                while (true) {
                    val count = source.read(buffer)
                    if (count < 0) break
                    total += count
                    if (total > expectedSize) throw IOException("Selected file changed while reading")
                    digest.update(buffer, 0, count)
                }
                if (total != expectedSize) throw IOException("Selected file changed while reading")
            }
            return digest.digest().joinToString("") { "%02x".format(it) }
        }
    }
}
