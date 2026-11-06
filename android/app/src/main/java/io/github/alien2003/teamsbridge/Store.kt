package io.github.alien2003.teamsbridge

import android.content.Context
import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.util.Log
import io.github.alien2003.teamsbridge.proto.ProtoException
import io.github.alien2003.teamsbridge.proto.Secret
import org.json.JSONArray
import org.json.JSONException
import java.io.IOException
import java.security.GeneralSecurityException
import java.security.KeyStore
import java.security.KeyStoreException
import java.security.ProviderException
import java.util.Base64
import javax.crypto.AEADBadTagException
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

internal const val TAG = "tbd"

/**
 * App state in one private SharedPreferences file. Only the pairing secret S is sensitive; it is
 * stored wrapped by an Android Keystore AES-256-GCM key ([SecretBox]), so a copy of the file without
 * the device's Keystore is useless. K and the topic are derived from S on demand and never stored.
 * Timestamps are local receive times in milliseconds.
 */
class Store private constructor(context: Context) {
    private val prefs: SharedPreferences = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    @Volatile
    private var cached: Secret? = null

    val isPaired: Boolean get() = prefs.contains(K_SECRET)

    /**
     * The pairing secret, or null if unpaired or it cannot be unwrapped right now. Only a blob that
     * can never be opened again (wrapping key gone or replaced, corrupt blob) clears the pairing; a
     * transient Keystore failure keeps it and the next call tries again.
     */
    fun secret(): Secret? {
        cached?.let { return it }
        val blob = prefs.getString(K_SECRET, null) ?: return null
        val secret = when (val r = SecretBox.open(blob)) {
            is SecretBox.Opened.Ok -> try {
                Secret.of(r.bytes)
            } catch (_: ProtoException) {
                null
            }
            SecretBox.Opened.Lost -> null
            SecretBox.Opened.Unavailable -> {
                Log.w(TAG, "Android Keystore unavailable; pairing kept, retrying on the next use")
                return null
            }
        }
        if (secret == null) {
            // E.g. app data restored onto another device: the Keystore key did not come along.
            Log.w(TAG, "stored pairing secret cannot be unwrapped; clearing the pairing")
            unpair()
            return null
        }
        cached = secret
        return secret
    }

    /** Stores a new pairing and resets everything that belonged to the previous one. */
    fun pair(secret: Secret, nowMillis: Long) {
        val blob = SecretBox.seal(secret.bytes())
        val e = prefs.edit()
        PAIRING_KEYS.forEach(e::remove)
        e.putString(K_SECRET, blob).putLong(K_PAIRED_AT, nowMillis).commit()
        cached = secret
    }

    /** Forgets the pairing: wrapped secret, its Keystore key, timestamps and replay window. */
    fun unpair() {
        cached = null
        val e = prefs.edit()
        PAIRING_KEYS.forEach(e::remove)
        e.commit()
        SecretBox.destroy()
    }

    var subscribed: Boolean
        get() = prefs.getBoolean(K_SUBSCRIBED, false)
        set(v) {
            prefs.edit().putBoolean(K_SUBSCRIBED, v).apply()
        }

    val pairedAt: Long? get() = long(K_PAIRED_AT)
    val pairPingAt: Long? get() = long(K_PAIR_PING_AT)
    val lastHeartbeatAt: Long? get() = long(K_LAST_HB_AT)
    val lastMessageAt: Long? get() = long(K_LAST_MSG_AT)
    val lastAlertAt: Long? get() = long(K_LAST_ALERT_AT)
    val watchdogAlertAt: Long? get() = long(K_WATCHDOG_ALERT_AT)

    fun setPairPingAt(v: Long) = put(K_PAIR_PING_AT, v)
    fun setLastHeartbeatAt(v: Long) = put(K_LAST_HB_AT, v)
    fun setLastMessageAt(v: Long) = put(K_LAST_MSG_AT, v)
    fun setLastAlertAt(v: Long) = put(K_LAST_ALERT_AT, v)
    fun setWatchdogAlertAt(v: Long) = put(K_WATCHDOG_ALERT_AT, v)

    /** True once anything authenticated arrived for the current pairing. */
    val hasContact: Boolean get() = pairPingAt != null || lastHeartbeatAt != null || lastMessageAt != null || lastAlertAt != null

    /** Package chosen under "Open Teams in:", or null for the system default. */
    var handlerPackage: String?
        get() = prefs.getString(K_HANDLER, null)
        set(v) {
            prefs.edit().apply { if (v == null) remove(K_HANDLER) else putString(K_HANDLER, v) }.apply()
        }

    fun replayIds(): List<String> {
        val s = prefs.getString(K_REPLAY, null) ?: return emptyList()
        return try {
            val a = JSONArray(s)
            List(a.length()) { a.getString(it) }
        } catch (_: JSONException) {
            emptyList()
        }
    }

    /** Persists synchronously: the replay window must survive the process being killed right after. */
    fun saveReplayIds(ids: List<String>) {
        prefs.edit().putString(K_REPLAY, JSONArray(ids).toString()).commit()
    }

    fun registerListener(l: SharedPreferences.OnSharedPreferenceChangeListener) = prefs.registerOnSharedPreferenceChangeListener(l)

    fun unregisterListener(l: SharedPreferences.OnSharedPreferenceChangeListener) = prefs.unregisterOnSharedPreferenceChangeListener(l)

    private fun long(k: String): Long? = if (prefs.contains(k)) prefs.getLong(k, 0) else null

    private fun put(k: String, v: Long) {
        prefs.edit().putLong(k, v).apply()
    }

    companion object {
        private const val PREFS = "tbd"
        private const val K_SECRET = "secret_wrapped"
        private const val K_PAIRED_AT = "paired_at"
        private const val K_PAIR_PING_AT = "pair_ping_at"
        private const val K_LAST_HB_AT = "last_hb_at"
        private const val K_LAST_MSG_AT = "last_msg_at"
        private const val K_LAST_ALERT_AT = "last_alert_at"
        private const val K_WATCHDOG_ALERT_AT = "watchdog_alert_at"
        private const val K_SUBSCRIBED = "subscribed"
        private const val K_REPLAY = "replay_ids"
        private const val K_HANDLER = "handler_package"
        private val PAIRING_KEYS = listOf(
            K_SECRET, K_PAIRED_AT, K_PAIR_PING_AT, K_LAST_HB_AT, K_LAST_MSG_AT, K_LAST_ALERT_AT,
            K_WATCHDOG_ALERT_AT, K_SUBSCRIBED, K_REPLAY,
        )

        @Volatile
        private var instance: Store? = null

        fun get(context: Context): Store = instance ?: synchronized(this) {
            instance ?: Store(context.applicationContext).also { instance = it }
        }
    }
}

/**
 * Wraps the pairing secret with a non-exportable AES-256-GCM key in the Android Keystore (no user
 * authentication, so the FCM service can decrypt in the background while the screen is locked).
 * Replaces the deprecated androidx.security:security-crypto with ~40 lines and no dependency.
 */
internal object SecretBox {
    private const val KEYSTORE = "AndroidKeyStore"
    private const val ALIAS = "tbd.secret.wrap.v1"
    private const val TRANSFORMATION = "AES/GCM/NoPadding"
    private const val IV_SIZE = 12
    private const val TAG_BITS = 128
    private val AAD = "tbd-secret-v1".toByteArray(Charsets.US_ASCII)

    /** What [open] found. */
    sealed interface Opened {
        class Ok(val bytes: ByteArray) : Opened

        /** Can never be opened again: the wrapping key is gone or was replaced, or the blob is corrupt. */
        data object Lost : Opened

        /** The Keystore failed this time (it does, sporadically); key and blob may well be fine. */
        data object Unavailable : Opened
    }

    // Every Keystore failure surfaces as a GeneralSecurityException (or the unchecked ProviderException).
    private fun keyStore(): KeyStore = try {
        KeyStore.getInstance(KEYSTORE).apply { load(null) }
    } catch (e: IOException) {
        throw KeyStoreException("AndroidKeyStore cannot be loaded", e)
    }

    private fun key(create: Boolean): SecretKey? {
        (keyStore().getKey(ALIAS, null) as? SecretKey)?.let { return it }
        if (!create) return null
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE)
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return gen.generateKey()
    }

    /**
     * base64(iv || ciphertext || tag). Throws [GeneralSecurityException] or [ProviderException] if
     * the Keystore fails.
     */
    fun seal(plain: ByteArray): String {
        val c = Cipher.getInstance(TRANSFORMATION)
        c.init(Cipher.ENCRYPT_MODE, key(create = true))
        c.updateAAD(AAD)
        val ct = c.doFinal(plain)
        return Base64.getEncoder().encodeToString(c.iv + ct)
    }

    fun open(blob: String): Opened {
        val all = try {
            Base64.getDecoder().decode(blob)
        } catch (_: IllegalArgumentException) {
            return Opened.Lost
        }
        if (all.size < IV_SIZE + TAG_BITS / 8) return Opened.Lost
        return try {
            // getKey returns null only when the alias does not exist; other failures throw.
            val k = key(create = false) ?: return Opened.Lost
            val c = Cipher.getInstance(TRANSFORMATION)
            c.init(Cipher.DECRYPT_MODE, k, GCMParameterSpec(TAG_BITS, all, 0, IV_SIZE))
            c.updateAAD(AAD)
            Opened.Ok(c.doFinal(all, IV_SIZE, all.size - IV_SIZE))
        } catch (_: AEADBadTagException) {
            Opened.Lost // a different key under our alias: this blob is unrecoverable
        } catch (_: KeyPermanentlyInvalidatedException) {
            Opened.Lost
        } catch (_: GeneralSecurityException) {
            Opened.Unavailable
        } catch (_: ProviderException) {
            Opened.Unavailable
        }
    }

    fun destroy() {
        try {
            keyStore().deleteEntry(ALIAS)
        } catch (_: GeneralSecurityException) {
        } catch (_: ProviderException) {
        }
    }
}
