package io.github.alien2003.teamsbridge.proto

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.security.SecureRandom

class SecretTest {
    @Test
    fun codeFormsFromVectors() {
        val s = Secret.of(TestVectors.secret)
        assertEquals(TestVectors.root.getString("code"), s.code)
        assertEquals(TestVectors.root.getString("display_code"), s.displayCode)
        assertEquals(TestVectors.root.getString("qr_text"), s.qrText)
    }

    @Test
    fun everyCodeInputOfTheVectors() {
        val inputs = TestVectors.codeInputs
        assertTrue("vectors must contain code inputs", inputs.size >= 10)
        for (v in inputs) {
            val input = v.getString("input")
            val note = v.optString("note")
            val parsed = Secret.parseOrNull(input)
            if (v.getBoolean("valid")) {
                assertNotNull("valid input rejected: $note", parsed)
                assertEquals(note, v.getString("secret_hex"), parsed!!.bytes().toHex())
            } else {
                assertNull("invalid input accepted: $note", parsed)
                assertThrows(note, ProtoException::class.java) { Secret.parse(input) }
            }
        }
    }

    @Test
    fun parsingRulesBeyondTheVectors() {
        val code = TestVectors.root.getString("code")
        val hex = TestVectors.root.getString("secret_hex")
        val ok = listOf(
            "TBD:V1:$code", // prefix in any case
            "Tbd:v1:${code.lowercase()}",
            "\t\n $code \r\n", // Go's TrimSpace: ASCII whitespace…
            " $code ", // …and Unicode White_Space at the ends
            "tbd:v1:" + code.chunked(4).joinToString("-"), // QR prefix plus dashes
            code.chunked(1).joinToString(" "), // spaces anywhere
            "--$code--",
        )
        for (input in ok) assertEquals(input, hex, Secret.parse(input).bytes().toHex())

        val bad = listOf(
            code.substring(0, 13) + "\t" + code.substring(13), // only '-' and ' ' are ignored inside
            code.substring(0, 25) + "Ａ", // full-width letter: not folded, not base32
            code.substring(0, 25) + "0", // '0', '1', '8', '9' are not in the alphabet
            code.substring(0, 25) + "8",
            "tbd:v2:$code", // other protocol version
            "tbd:v1:tbd:v1:$code", // the prefix is stripped once
            code + "=", // no padding character
            " tbd:v1 :$code",
        )
        for (input in bad) assertNull(input, Secret.parseOrNull(input))
    }

    @Test
    fun canonicalOnlyEveryTrailingCharacter() {
        // The last character carries 3 data bits and 2 zero bits: of the 32 possible last characters
        // exactly 8 are canonical for a given prefix.
        val code = TestVectors.root.getString("code")
        val accepted = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567".count { Secret.parseOrNull(code.dropLast(1) + it) != null }
        assertEquals(8, accepted)
    }

    @Test
    fun randomRoundTrips() {
        val rnd = SecureRandom()
        repeat(500) {
            val raw = ByteArray(16).also(rnd::nextBytes)
            val s = Secret.of(raw)
            assertEquals(Proto.CODE_LEN, s.code.length)
            assertTrue(Regex("[A-Z2-7]{4}(-[A-Z2-7]{4}){5}-[A-Z2-7]{2}").matches(s.displayCode))
            assertEquals(s, Secret.parse(s.code))
            assertEquals(s, Secret.parse(s.displayCode.lowercase()))
            assertEquals(s, Secret.parse(s.qrText))
            val topic = Hkdf.derive(raw, ByteArray(0), "tbd-topic".toByteArray(), 24)
            assertEquals(java.util.Base64.getUrlEncoder().withoutPadding().encodeToString(topic), s.topic())
        }
    }

    @Test
    fun base32AgainstRfc4648Vectors() {
        // RFC 4648 §10, without padding.
        val cases = mapOf(
            "" to "", "f" to "MY", "fo" to "MZXQ", "foo" to "MZXW6",
            "foob" to "MZXW6YQ", "fooba" to "MZXW6YTB", "foobar" to "MZXW6YTBOI",
        )
        for ((plain, enc) in cases) {
            assertEquals(enc, Base32.encode(plain.toByteArray()))
            assertEquals(plain, String(Base32.decode(enc)!!))
        }
        assertNull(Base32.decode("M")) // impossible lengths
        assertNull(Base32.decode("MZX"))
        assertNull(Base32.decode("MZXW6Y"))
    }

    @Test
    fun secretNeverPrinted() {
        val s = Secret.of(TestVectors.secret)
        assertFalse(s.toString().contains(s.code))
        assertThrows(ProtoException::class.java) { Secret.of(ByteArray(15)) }
    }
}
