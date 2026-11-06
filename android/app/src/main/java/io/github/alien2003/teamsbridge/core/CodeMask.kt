package io.github.alien2003.teamsbridge.core

import io.github.alien2003.teamsbridge.proto.Proto

/**
 * The dashed input mask of the Pair screen: whatever is typed or pasted is shown as
 * `XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XX`. Only base32 characters are kept (upper-cased), at most 26, and a
 * pasted QR text loses its `tbd:v1:` prefix. A dash is never trailing, so backspace always works.
 * The result is then parsed by [io.github.alien2003.teamsbridge.proto.Secret.parse].
 */
object CodeMask {
    fun format(input: CharSequence): String {
        var s = input.toString().trim()
        val p = Proto.QR_PREFIX
        if (s.length >= p.length && s.regionMatches(0, p, 0, p.length, ignoreCase = true)) s = s.substring(p.length)
        val raw = StringBuilder(Proto.CODE_LEN)
        for (ch in s) {
            val u = if (ch in 'a'..'z') ch - ('a' - 'A') else ch
            if (u in 'A'..'Z' || u in '2'..'7') {
                raw.append(u)
                if (raw.length == Proto.CODE_LEN) break
            }
        }
        return raw.chunked(4).joinToString("-")
    }

    /** True once the mask holds all 26 characters. */
    fun isComplete(formatted: CharSequence): Boolean = formatted.count { it != '-' } == Proto.CODE_LEN
}
