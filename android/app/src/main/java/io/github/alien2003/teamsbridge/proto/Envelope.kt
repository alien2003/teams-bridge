package io.github.alien2003.teamsbridge.proto

import java.util.Base64

/**
 * The FCM data field `c` = base64std(nonce || ciphertext || tag) (PROTOCOL.md §6, receiver rules
 * 1–2 of §7). Age and replay rules are applied separately by [ReplayFilter].
 */
object Envelope {

    /** base64-decodes, authenticates, decrypts and unpads [c]; returns the exact JSON bytes. */
    fun openJson(key: ByteArray, c: String): ByteArray {
        val sealed = try {
            Base64.getDecoder().decode(c)
        } catch (_: IllegalArgumentException) {
            throw ProtoException("c is not base64")
        }
        return Padding.unpad(Aead.open(key, sealed))
    }

    /** [openJson] plus [Message.parse]. Throws [ProtoException] for anything a receiver must drop. */
    fun open(key: ByteArray, c: String): Message = Message.parse(String(openJson(key, c), Charsets.UTF_8))

    /** The sending direction (only the daemon sends; used by the tests for round trips). */
    fun seal(key: ByteArray, nonce: ByteArray, json: ByteArray): String =
        Base64.getEncoder().encodeToString(Aead.seal(key, nonce, Padding.pad(json)))
}
