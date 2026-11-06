package io.github.alien2003.teamsbridge.core

import io.github.alien2003.teamsbridge.proto.Proto

/**
 * What a notification tap may open (BRIEF §6): only absolute http(s) URLs with a host; anything else,
 * including an empty `url`, becomes `https://teams.microsoft.com/v2/`. Messages are authenticated,
 * so this is defence in depth against a daemon-side mapping bug, not against an attacker.
 */
object UrlPolicy {
    // scheme://authority[path/query/fragment]; no userinfo, no whitespace or control characters.
    private val ALLOWED = Regex("^(?i:https?)://[^\\s/?#@\\\\]+(?:[/?#][^\\s]*)?$")

    fun sanitize(url: String?): String {
        val u = url?.trim().orEmpty()
        if (u.isEmpty() || u.any { it < ' ' || it == '\u007F' }) return Proto.DEFAULT_URL
        return if (ALLOWED.matches(u)) u else Proto.DEFAULT_URL
    }
}
