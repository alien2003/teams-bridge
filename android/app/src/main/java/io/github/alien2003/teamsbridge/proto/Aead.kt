package io.github.alien2003.teamsbridge.proto

import java.security.GeneralSecurityException
import javax.crypto.Cipher
import javax.crypto.spec.IvParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * ChaCha20-Poly1305 (RFC 8439) through JCA, AAD = ASCII `tbd:v1` (PROTOCOL.md §5).
 *
 * Android (API 28+, Conscrypt) names the cipher `ChaCha20/Poly1305/NoPadding`; plain JDKs (11+, the
 * JVM unit tests) call the same AEAD `ChaCha20-Poly1305`. Both take the 12-byte nonce as an
 * [IvParameterSpec] and append the 16-byte tag to the ciphertext, so the code is identical.
 */
object Aead {
    private val TRANSFORMATIONS = listOf("ChaCha20/Poly1305/NoPadding", "ChaCha20-Poly1305")
    private val AAD = Proto.AAD.toByteArray(Charsets.US_ASCII)

    /** The transformation this runtime supports, found once. */
    val transformation: String by lazy {
        TRANSFORMATIONS.firstOrNull { name ->
            try {
                Cipher.getInstance(name)
                true
            } catch (_: GeneralSecurityException) {
                false
            }
        } ?: throw IllegalStateException("no ChaCha20-Poly1305 provider")
    }

    /** Encrypts [padded] and returns `nonce || ciphertext || tag`. */
    fun seal(key: ByteArray, nonce: ByteArray, padded: ByteArray): ByteArray {
        require(key.size == Proto.KEY_SIZE && nonce.size == Proto.NONCE_SIZE)
        val c = Cipher.getInstance(transformation)
        c.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "ChaCha20"), IvParameterSpec(nonce))
        c.updateAAD(AAD)
        return nonce + c.doFinal(padded)
    }

    /**
     * Decrypts `nonce || ciphertext || tag` and returns the padded plaintext. The sealed size must
     * be 1052 or 2076 bytes; a wrong key, nonce, AAD or a modified byte fails authentication.
     */
    fun open(key: ByteArray, sealed: ByteArray): ByteArray {
        val l = sealed.size - Proto.NONCE_SIZE - Proto.TAG_SIZE
        if (l != Proto.SMALL_PAD && l != Proto.LARGE_PAD) throw ProtoException("invalid sealed length ${sealed.size}")
        if (key.size != Proto.KEY_SIZE) throw ProtoException("invalid key size")
        return try {
            val c = Cipher.getInstance(transformation)
            c.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "ChaCha20"), IvParameterSpec(sealed, 0, Proto.NONCE_SIZE))
            c.updateAAD(AAD)
            c.doFinal(sealed, Proto.NONCE_SIZE, sealed.size - Proto.NONCE_SIZE)
        } catch (e: GeneralSecurityException) {
            throw ProtoException("message authentication failed", e)
        }
    }
}
