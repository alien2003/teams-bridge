package io.github.alien2003.teamsbridge.proto

/**
 * `u16be(len(json)) || json || 0x00…` padded to 1024 or 2048 bytes (PROTOCOL.md §4).
 */
object Padding {

    /** The padded size for a JSON document of [n] bytes, or 0 if it does not fit. */
    fun sizeFor(n: Int): Int = when {
        n + 2 <= Proto.SMALL_PAD -> Proto.SMALL_PAD
        n + 2 <= Proto.LARGE_PAD -> Proto.LARGE_PAD
        else -> 0
    }

    /** Pads [json]; throws for a document larger than [Proto.MAX_PAYLOAD]. */
    fun pad(json: ByteArray): ByteArray {
        val size = sizeFor(json.size)
        if (size == 0) throw ProtoException("message does not fit the largest padding size")
        val out = ByteArray(size)
        out[0] = (json.size ushr 8).toByte()
        out[1] = json.size.toByte()
        json.copyInto(out, 2)
        return out
    }

    /**
     * Reverses [pad], strictly: the size must be 1024 or 2048, the declared length non-zero and
     * inside the buffer, 2048 only for a document that does not fit 1024, and the padding all zero.
     */
    fun unpad(p: ByteArray): ByteArray {
        if (p.size != Proto.SMALL_PAD && p.size != Proto.LARGE_PAD) {
            throw ProtoException("invalid padded length ${p.size}")
        }
        val n = ((p[0].toInt() and 0xFF) shl 8) or (p[1].toInt() and 0xFF)
        if (n == 0 || n > p.size - 2) throw ProtoException("invalid declared length $n")
        if (p.size == Proto.LARGE_PAD && n + 2 <= Proto.SMALL_PAD) {
            throw ProtoException("$n bytes should have used the small size")
        }
        for (i in 2 + n until p.size) {
            if (p[i] != 0.toByte()) throw ProtoException("non-zero padding")
        }
        return p.copyOfRange(2, 2 + n)
    }
}
