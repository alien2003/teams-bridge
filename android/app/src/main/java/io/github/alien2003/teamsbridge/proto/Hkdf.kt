package io.github.alien2003.teamsbridge.proto

import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/**
 * HKDF-SHA256 (RFC 5869), written by hand over JCA `HmacSHA256` because Android has no HKDF API
 * before API 36 and the app takes no crypto library (BRIEF §3).
 */
object Hkdf {
    private const val HMAC = "HmacSHA256"
    private const val HASH_LEN = 32

    /** HKDF-Extract. An empty salt means HashLen zero bytes (RFC 5869 §2.2). */
    fun extract(salt: ByteArray, ikm: ByteArray): ByteArray {
        // SecretKeySpec rejects an empty key; HMAC pads the key with zeros anyway, so 32 zero bytes
        // give exactly the RFC's result.
        val key = if (salt.isEmpty()) ByteArray(HASH_LEN) else salt
        val mac = Mac.getInstance(HMAC)
        mac.init(SecretKeySpec(key, HMAC))
        return mac.doFinal(ikm)
    }

    /** HKDF-Expand to [length] bytes (at most 255 * 32). */
    fun expand(prk: ByteArray, info: ByteArray, length: Int): ByteArray {
        require(length in 0..255 * HASH_LEN) { "HKDF length out of range" }
        val mac = Mac.getInstance(HMAC)
        mac.init(SecretKeySpec(prk, HMAC))
        val out = ByteArray(length)
        var t = ByteArray(0)
        var pos = 0
        var counter = 1
        while (pos < length) {
            mac.update(t)
            mac.update(info)
            mac.update(counter.toByte())
            t = mac.doFinal()
            val n = minOf(t.size, length - pos)
            t.copyInto(out, pos, 0, n)
            pos += n
            counter++
        }
        return out
    }

    /** Extract-then-expand. */
    fun derive(ikm: ByteArray, salt: ByteArray, info: ByteArray, length: Int): ByteArray =
        expand(extract(salt, ikm), info, length)
}
