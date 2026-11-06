package io.github.alien2003.teamsbridge.proto

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class HkdfTest {
    private val ikm = ByteArray(22) { 0x0b }

    @Test
    fun rfc5869TestCase1() {
        val salt = ByteArray(13) { it.toByte() }
        val info = ByteArray(10) { (0xf0 + it).toByte() }
        val prk = Hkdf.extract(salt, ikm)
        assertEquals("077709362c2e32df0ddc3f0dc47bba6390b6c73bb50f9c3122ec844ad7c2b3e5", prk.toHex())
        assertEquals(
            "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865",
            Hkdf.expand(prk, info, 42).toHex(),
        )
    }

    @Test
    fun rfc5869TestCase3EmptySaltAndInfo() {
        // The protocol's own case: salt = "" means 32 zero bytes.
        val prk = Hkdf.extract(ByteArray(0), ikm)
        assertEquals("19ef24a32c717b167f33a91d6f648bdf96596776afdb6377ac434c1c293ccb04", prk.toHex())
        assertEquals(
            "8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8",
            Hkdf.derive(ikm, ByteArray(0), ByteArray(0), 42).toHex(),
        )
        assertEquals(prk.toHex(), Hkdf.extract(ByteArray(32), ikm).toHex())
    }

    @Test
    fun lengthsAndPrefixes() {
        val prk = Hkdf.extract(ByteArray(0), ikm)
        assertEquals(0, Hkdf.expand(prk, ByteArray(0), 0).size)
        val long = Hkdf.expand(prk, "x".toByteArray(), 255 * 32)
        assertEquals(255 * 32, long.size)
        // HKDF output is a prefix of the longer output for the same PRK and info.
        assertEquals(long.copyOf(33).toHex(), Hkdf.expand(prk, "x".toByteArray(), 33).toHex())
        assertThrows(IllegalArgumentException::class.java) { Hkdf.expand(prk, ByteArray(0), 255 * 32 + 1) }
    }

    @Test
    fun keyAndTopicFromVectors() {
        val s = Secret.of(TestVectors.secret)
        assertEquals(TestVectors.root.getString("key_hex"), s.key().toHex())
        val topic = s.topic()
        assertEquals(TestVectors.root.getString("topic"), topic)
        assertEquals(Proto.TOPIC_LEN, topic.length)
        // FCM topic name syntax.
        assertTrue(Regex("[a-zA-Z0-9_.~%-]{1,900}").matches(topic))
    }
}
