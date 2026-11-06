package io.github.alien2003.teamsbridge.proto

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

/** PROTOCOL.md §4, including the 1024/2048 boundaries and the receiver's strictness. */
class PaddingTest {
    private fun doc(n: Int) = ByteArray(n) { 'a'.code.toByte() }

    @Test
    fun sizeBoundaries() {
        assertEquals(1024, Padding.sizeFor(1))
        assertEquals(1024, Padding.sizeFor(1022)) // 1022 + 2 = 1024: still small
        assertEquals(2048, Padding.sizeFor(1023))
        assertEquals(2048, Padding.sizeFor(Proto.MAX_PAYLOAD)) // 2046
        assertEquals(0, Padding.sizeFor(Proto.MAX_PAYLOAD + 1))
        assertThrows(ProtoException::class.java) { Padding.pad(doc(2047)) }
    }

    @Test
    fun roundTripsAtTheBoundaries() {
        for (n in listOf(1, 2, 255, 256, 1021, 1022, 1023, 1024, 2045, 2046)) {
            val d = doc(n)
            val p = Padding.pad(d)
            assertEquals("n=$n", if (n <= 1022) 1024 else 2048, p.size)
            assertEquals("n=$n high byte", (n ushr 8).toByte(), p[0])
            assertEquals("n=$n low byte", n.toByte(), p[1])
            assertArrayEquals("n=$n", d, Padding.unpad(p))
        }
    }

    @Test
    fun receiverIsStrict() {
        fun padded(size: Int, declared: Int, fill: Int = declared): ByteArray {
            val p = ByteArray(size)
            p[0] = (declared ushr 8).toByte()
            p[1] = declared.toByte()
            for (i in 2 until 2 + fill) p[i] = 'x'.code.toByte()
            return p
        }
        // Wrong total sizes.
        for (size in listOf(0, 1, 1023, 1025, 1536, 2047, 2049, 4096)) {
            assertThrows("size $size", ProtoException::class.java) { Padding.unpad(ByteArray(size).also { if (size >= 2) it[1] = 1 }) }
        }
        // Declared length 0 or beyond the buffer.
        assertThrows(ProtoException::class.java) { Padding.unpad(padded(1024, 0)) }
        assertThrows(ProtoException::class.java) { Padding.unpad(padded(1024, 1023, 1022)) }
        assertThrows(ProtoException::class.java) { Padding.unpad(padded(2048, 2047, 2046)) }
        assertThrows(ProtoException::class.java) { Padding.unpad(padded(1024, 0xFFFF, 1022)) }
        // A document that fits 1024 must not use 2048 — 1022 bytes is the largest such document.
        assertThrows(ProtoException::class.java) { Padding.unpad(padded(2048, 1022)) }
        assertThrows(ProtoException::class.java) { Padding.unpad(padded(2048, 1)) }
        assertEquals(1023, Padding.unpad(padded(2048, 1023)).size)
        // Non-zero padding anywhere, including the very last byte.
        for (i in listOf(12, 500, 1023)) {
            val p = padded(1024, 10).also { it[i] = 1 }
            assertThrows("byte $i", ProtoException::class.java) { Padding.unpad(p) }
        }
        assertEquals(1022, Padding.unpad(padded(1024, 1022)).size)
        assertEquals(2046, Padding.unpad(padded(2048, 2046)).size)
    }

    @Test
    fun sealedSizes() {
        val key = TestVectors.key
        val nonce = ByteArray(Proto.NONCE_SIZE)
        // nonce (12) + padded + tag (16) → 1052 / 2076 bytes → 1404 / 2768 base64 characters.
        val small = Envelope.seal(key, nonce, doc(1022))
        val large = Envelope.seal(key, nonce, doc(1023))
        assertEquals(1404, small.length)
        assertEquals(2768, large.length)
        assertEquals(1052, java.util.Base64.getDecoder().decode(small).size)
        assertEquals(2076, java.util.Base64.getDecoder().decode(large).size)
        assertArrayEquals(doc(1022), Envelope.openJson(key, small))
        assertArrayEquals(doc(1023), Envelope.openJson(key, large))
        // Both stay far below FCM's 4096-byte data limit.
        assertEquals(true, large.length < 4096)
    }
}
