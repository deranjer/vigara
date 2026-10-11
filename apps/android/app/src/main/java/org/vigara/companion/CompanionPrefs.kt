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

class CompanionPrefs(context: Context) {
    private val prefs = context.getSharedPreferences("companion", Context.MODE_PRIVATE)
    var server: String
        get() = prefs.getString("server", "") ?: ""
        set(value) = prefs.edit().putString("server", value).apply()
    var agentId: String
        get() = prefs.getString("agent", "") ?: ""
        set(value) = prefs.edit().putString("agent", value).apply()
    var pendingTelemetryBody: String
        get() = prefs.getString("pending_telemetry_body", "") ?: ""
        set(value) = prefs.edit().putString("pending_telemetry_body", value).apply()
    var credential: String
        get() {
            val encoded = prefs.getString("credential", null) ?: return ""
            return try {
                val bytes = Base64.decode(encoded, Base64.NO_WRAP)
                val cipher = Cipher.getInstance("AES/GCM/NoPadding")
                cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, bytes.copyOfRange(0, 12)))
                String(cipher.doFinal(bytes.copyOfRange(12, bytes.size)), Charsets.UTF_8)
            } catch (_: Exception) { "" }
        }
        set(value) {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, key())
            val bytes = cipher.iv + cipher.doFinal(value.toByteArray(Charsets.UTF_8))
            prefs.edit().putString("credential", Base64.encodeToString(bytes, Base64.NO_WRAP)).apply()
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

    companion object { private const val KEY_ALIAS = "vigara-agent-credential-v1" }
}
