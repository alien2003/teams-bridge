package io.github.alien2003.teamsbridge.proto

import java.util.Base64

/**
 * The 16-byte pairing secret S (PROTOCOL.md §1–2). [toString] never reveals it.
 */
class Secret private constructor(private val s: ByteArray) {

    /** A copy of the raw secret. */
    fun bytes(): ByteArray = s.copyOf()

    /** The canonical 26-character code (RFC 4648 base32, uppercase, no padding). */
    val code: String get() = Base32.encode(s)

    /** `XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XX`. */
    val displayCode: String get() = code.chunked(4).joinToString("-")

    /** The text of the pairing QR code. */
    val qrText: String get() = Proto.QR_PREFIX + code

    /** K = HKDF-SHA256(ikm=S, salt="", info="tbd-enc", L=32). */
    fun key(): ByteArray = Hkdf.derive(s, ByteArray(0), Proto.INFO_ENC.toByteArray(Charsets.US_ASCII), Proto.KEY_SIZE)

    /** topic = base64url_nopad(HKDF-SHA256(ikm=S, salt="", info="tbd-topic", L=24)), 32 characters. */
    fun topic(): String {
        val raw = Hkdf.derive(s, ByteArray(0), Proto.INFO_TOPIC.toByteArray(Charsets.US_ASCII), Proto.TOPIC_BYTES)
        return Base64.getUrlEncoder().withoutPadding().encodeToString(raw)
    }

    override fun equals(other: Any?): Boolean = other is Secret && other.s.contentEquals(s)

    override fun hashCode(): Int = s.contentHashCode()

    override fun toString(): String = "Secret(redacted)"

    companion object {
        /** Wraps 16 raw bytes. */
        fun of(bytes: ByteArray): Secret {
            if (bytes.size != Proto.SECRET_SIZE) throw ProtoException("secret must be ${Proto.SECRET_SIZE} bytes")
            return Secret(bytes.copyOf())
        }

        /**
         * Parses a typed or scanned code exactly like Go's `proto.ParseCode`: surrounding whitespace
         * is trimmed, an optional `tbd:v1:` prefix (any case) is stripped, dashes and spaces are
         * ignored, ASCII letters are upper-cased, and only the canonical 26-character form of a
         * 16-byte secret is accepted (non-zero trailing bits are rejected).
         */
        fun parse(input: String): Secret {
            var s = trimGoSpace(input)
            val p = Proto.QR_PREFIX
            if (s.length >= p.length && s.regionMatches(0, p, 0, p.length, ignoreCase = true)) {
                s = s.substring(p.length)
            }
            val b = StringBuilder(s.length)
            for (ch in s) {
                when (ch) {
                    '-', ' ' -> Unit
                    in 'a'..'z' -> b.append(ch - ('a' - 'A'))
                    else -> b.append(ch)
                }
            }
            val c = b.toString()
            if (c.length != Proto.CODE_LEN) {
                throw ProtoException("pairing code must have ${Proto.CODE_LEN} characters, got ${c.length}")
            }
            val raw = Base32.decode(c)
            // Re-encoding catches non-zero trailing bits in the last character (canonical form only).
            if (raw == null || raw.size != Proto.SECRET_SIZE || Base32.encode(raw) != c) {
                throw ProtoException("pairing code is not canonical base32")
            }
            return Secret(raw)
        }

        /** [parse], or null for an invalid code. */
        fun parseOrNull(input: String): Secret? = try {
            parse(input)
        } catch (_: ProtoException) {
            null
        }

        /** Go's `strings.TrimSpace`: trims Unicode White_Space (`unicode.IsSpace`) at both ends. */
        internal fun trimGoSpace(s: String): String {
            var start = 0
            var end = s.length
            while (start < end && isGoSpace(s[start])) start++
            while (end > start && isGoSpace(s[end - 1])) end--
            return s.substring(start, end)
        }

        /** Go's `unicode.IsSpace` (every White_Space code point is in the BMP). */
        internal fun isGoSpace(c: Char): Boolean = when (c) {
            '\t', '\n', '\u000B', '\u000C', '\r', ' ', '\u0085', ' ' -> true
            else -> c.code > 0xFF && when (Character.getType(c).toByte()) {
                Character.SPACE_SEPARATOR, Character.LINE_SEPARATOR, Character.PARAGRAPH_SEPARATOR -> true
                else -> false
            }
        }
    }
}

/** RFC 4648 base32 with the standard alphabet and no padding. */
internal object Base32 {
    private const val ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

    fun encode(data: ByteArray): String {
        val out = StringBuilder((data.size * 8 + 4) / 5)
        var buffer = 0
        var bits = 0
        for (byte in data) {
            buffer = (buffer shl 8) or (byte.toInt() and 0xFF)
            bits += 8
            while (bits >= 5) {
                out.append(ALPHABET[(buffer ushr (bits - 5)) and 0x1F])
                bits -= 5
            }
        }
        if (bits > 0) out.append(ALPHABET[(buffer shl (5 - bits)) and 0x1F])
        return out.toString()
    }

    /** Decodes unpadded base32, or returns null for a character outside the alphabet or a length no
     * encoder produces. Trailing bits are not checked here (callers compare a re-encoding). */
    fun decode(s: String): ByteArray? {
        if ((s.length * 5) % 8 >= 5) return null
        val out = ByteArray(s.length * 5 / 8)
        var buffer = 0
        var bits = 0
        var pos = 0
        for (ch in s) {
            val v = ALPHABET.indexOf(ch)
            if (v < 0) return null
            buffer = (buffer shl 5) or v
            bits += 5
            if (bits >= 8) {
                out[pos++] = (buffer ushr (bits - 8)).toByte()
                bits -= 8
            }
            buffer = buffer and ((1 shl bits) - 1)
        }
        return out
    }
}
