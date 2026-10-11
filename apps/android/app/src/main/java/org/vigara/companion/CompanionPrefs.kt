package org.vigara.companion

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import java.util.UUID

class CompanionPrefs(context: Context) {
    private val prefs = context.getSharedPreferences("companion", Context.MODE_PRIVATE)
    val server: String
        get() = prefs.getString("server", "") ?: ""
    val agentId: String
        get() = prefs.getString("agent", "") ?: ""
    val generation: String
        get() = prefs.getString("generation", "") ?: ""

    fun setEnrollment(server: String, agentId: String, credential: String): String {
        synchronized(enrollmentLock) {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, key())
            val encrypted = Base64.encodeToString(cipher.iv + cipher.doFinal(credential.toByteArray(Charsets.UTF_8)), Base64.NO_WRAP)
            val nextGeneration = UUID.randomUUID().toString()
            check(prefs.edit().putString("server", server).putString("agent", agentId)
                .putString("credential", encrypted).putString("generation", nextGeneration)
                .remove("pending_telemetry_body").commit())
            return nextGeneration
        }
    }

    fun ensureGeneration(): String {
        synchronized(enrollmentLock) {
            generation.takeIf { it.isNotBlank() }?.let { return it }
            val next = UUID.randomUUID().toString()
            check(prefs.edit().putString("generation", next).commit())
            return next
        }
    }
    val pendingTelemetryBody: String
        get() = prefs.getString("pending_telemetry_body", "") ?: ""

    data class EnrollmentSnapshot(val server: String, val agentId: String, val credential: String, val generation: String, val pendingTelemetryBody: String)

    fun enrollmentSnapshot(): EnrollmentSnapshot = synchronized(enrollmentLock) { EnrollmentSnapshot(server, agentId, credential, generation, pendingTelemetryBody) }

    fun storeTelemetry(generation: String, body: String): Boolean {
        synchronized(enrollmentLock) {
            if (this.generation != generation) return false
            return prefs.edit().putString("pending_telemetry_body", body).commit()
        }
    }

    fun clearTelemetry(generation: String, body: String) {
        synchronized(enrollmentLock) {
            if (this.generation == generation && pendingTelemetryBody == body) prefs.edit().remove("pending_telemetry_body").commit()
        }
    }
    val credential: String
        get() {
            val encoded = prefs.getString("credential", null) ?: return ""
            return try {
                val bytes = Base64.decode(encoded, Base64.NO_WRAP)
                val cipher = Cipher.getInstance("AES/GCM/NoPadding")
                cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, bytes.copyOfRange(0, 12)))
                String(cipher.doFinal(bytes.copyOfRange(12, bytes.size)), Charsets.UTF_8)
            } catch (_: Exception) { "" }
        }

    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
            init(KeyGenParameterSpec.Builder(KEY_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true).build())
            generateKey()
        }
    }

    companion object {
        private const val KEY_ALIAS = "vigara-agent-credential-v1"
        private val enrollmentLock = Any()
    }
}
