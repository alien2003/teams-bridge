package io.github.alien2003.teamsbridge.proto

import org.json.JSONException
import org.json.JSONObject
import org.json.JSONTokener

/**
 * The JSON document inside a sealed payload (PROTOCOL.md §3). [toString] omits title, body, url and
 * tag so that a message can never end up in a log by accident.
 */
data class Message(
    val v: Int,
    val t: String,
    val id: String,
    val ts: Long,
    val title: String = "",
    val body: String = "",
    val url: String = "",
    val tag: String = "",
    val kind: String = "",
) {
    override fun toString(): String = "Message(t=$t, id=$id, ts=$ts, kind=$kind)"

    companion object {
        const val MSG = "msg"
        const val ALERT = "alert"
        const val HB = "hb"
        const val PAIR = "pair"
        private val TYPES = setOf(MSG, ALERT, HB, PAIR)

        const val KIND_SESSION_EXPIRED = "session_expired"
        const val KIND_BROWSER_DEAD = "browser_dead"
        const val KIND_DAEMON_STOPPING = "daemon_stopping"

        /**
         * Parses and validates a message document. For well-formed RFC 8259 documents with the
         * lowercase keys the Go encoder emits it matches Go's `json.Unmarshal` into `proto.Message`
         * plus `Validate`: missing fields and `null` are zero values, unknown fields are ignored, a
         * field of the wrong JSON type is an error, `v` must be the integer 1, `t` one of
         * msg/alert/hb/pair and `id` non-empty, and trailing non-whitespace is an error.
         *
         * It is not a strict JSON validator: Go matches keys case-insensitively, and Android's
         * platform org.json accepts comments, unquoted or single-quoted strings, '=' / ';'
         * separators and hex/octal integers (the JVM tests run JSON-java instead). That is harmless:
         * only plaintext that authenticated under K, i.e. written by the daemon, reaches the parser.
         */
        fun parse(json: String): Message {
            val obj = try {
                val tok = JSONTokener(json)
                val value = tok.nextValue()
                if (value !is JSONObject) throw ProtoException("message is not a JSON object")
                if (tok.nextClean() != 0.toChar()) throw ProtoException("trailing data after the JSON object")
                value
            } catch (_: JSONException) {
                // The JSONException text may quote the input; it is deliberately not carried along.
                throw ProtoException("message is not valid JSON")
            }
            val m = Message(
                v = intField(obj, "v").let { if (it in Int.MIN_VALUE..Int.MAX_VALUE) it.toInt() else 0 },
                t = stringField(obj, "t"),
                id = stringField(obj, "id"),
                ts = intField(obj, "ts"),
                title = stringField(obj, "title"),
                body = stringField(obj, "body"),
                url = stringField(obj, "url"),
                tag = stringField(obj, "tag"),
                kind = stringField(obj, "kind"),
            )
            if (m.v != Proto.VERSION) throw ProtoException("unsupported version")
            if (m.t !in TYPES) throw ProtoException("unknown message type")
            if (m.id.isEmpty()) throw ProtoException("empty id")
            return m
        }

        private fun raw(obj: JSONObject, name: String): Any? {
            val x = obj.opt(name)
            return if (x == null || x === JSONObject.NULL) null else x
        }

        private fun stringField(obj: JSONObject, name: String): String = when (val x = raw(obj, name)) {
            null -> ""
            is String -> x
            else -> throw ProtoException("field $name must be a string")
        }

        private fun intField(obj: JSONObject, name: String): Long = when (val x = raw(obj, name)) {
            null -> 0L
            is Int -> x.toLong()
            is Long -> x
            else -> throw ProtoException("field $name must be an integer")
        }
    }
}
