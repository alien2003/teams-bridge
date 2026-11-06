// Package proto is the Kotlin side of the frozen tbd v1 wire protocol (proto/PROTOCOL.md, BRIEF §3).
// It is pure Kotlin/JVM (no Android classes) so that the JVM unit tests run it unchanged against
// proto/vectors.json. Any change here is a protocol version bump.
package io.github.alien2003.teamsbridge.proto

/** Protocol constants (PROTOCOL.md). */
object Proto {
    const val VERSION = 1
    const val SECRET_SIZE = 16
    const val CODE_LEN = 26
    const val QR_PREFIX = "tbd:v1:"
    const val AAD = "tbd:v1"
    const val KEY_SIZE = 32
    const val NONCE_SIZE = 12
    const val TAG_SIZE = 16
    const val SMALL_PAD = 1024
    const val LARGE_PAD = 2048

    /** Largest JSON document that fits [LARGE_PAD] (2 bytes go to the length prefix). */
    const val MAX_PAYLOAD = LARGE_PAD - 2
    const val TOPIC_LEN = 32

    /** Receiver rule 3: messages whose `ts` is more than this in the past are dropped. */
    const val MAX_AGE_MILLIS = 24L * 60 * 60 * 1000

    /** Receiver rule 4: how many accepted ids are remembered for de-duplication. */
    const val REPLAY_SIZE = 256

    /** Where a tap goes when a message carries no usable URL. */
    const val DEFAULT_URL = "https://teams.microsoft.com/v2/"

    internal const val INFO_ENC = "tbd-enc"
    internal const val INFO_TOPIC = "tbd-topic"
    internal const val TOPIC_BYTES = 24
}

/**
 * Any protocol-level rejection: bad code, bad padding, failed authentication, invalid message.
 * Messages are fixed strings that never contain decrypted content, so they are safe to log.
 */
class ProtoException(message: String, cause: Throwable? = null) : Exception(message, cause)
