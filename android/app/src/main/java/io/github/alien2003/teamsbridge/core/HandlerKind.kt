package io.github.alien2003.teamsbridge.core

/**
 * Labels for the "Open Teams in:" list (BRIEF §6). An activity whose resolved intent filter names a
 * host (it matched the Teams host itself, not just the `https` scheme) is an installed web app: a
 * WebAPK/PWA. The native Teams app also declares the host; it is labelled separately so it is not
 * mistaken for the PWA. Everything that only matches the scheme is a browser.
 */
enum class HandlerKind(val label: String) {
    PWA("PWA"),
    APP("Teams app"),
    BROWSER("Browser"),
    ;

    companion object {
        const val TEAMS_APP_PACKAGE = "com.microsoft.teams"

        /** The URIs the Settings screen resolves; a PWA may be scoped to `/v2/` only. */
        val QUERY_URIS = listOf("https://teams.microsoft.com/", "https://teams.microsoft.com/v2/")

        fun classify(packageName: String, filterMatchesHost: Boolean): HandlerKind = when {
            !filterMatchesHost -> BROWSER
            packageName == TEAMS_APP_PACKAGE -> APP
            else -> PWA
        }
    }
}
