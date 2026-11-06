// Package core holds app logic that needs no Android classes, so the JVM unit tests cover it.
package io.github.alien2003.teamsbridge.core

/**
 * The local watchdog (BRIEF §6): the daemon sends a heartbeat every 6 h; if none arrived for 12 h the
 * phone raises a local alert, at most once per 12 h. Before the first heartbeat after pairing the
 * pairing time is the reference.
 */
object Watchdog {
    const val CHECK_INTERVAL_HOURS = 1L
    const val STALE_MILLIS = 12L * 60 * 60 * 1000
    const val REPEAT_MILLIS = 12L * 60 * 60 * 1000

    enum class Kind {
        /** No pairing secret stored: nothing to watch. */
        UNPAIRED,

        /** Paired, no heartbeat yet, pairing is less than 12 h ago. */
        WAITING,

        /** A heartbeat arrived within the last 12 h. */
        OK,

        /** Nothing for more than 12 h. */
        STALE,
    }

    /** What the Status screen shows and what the worker acts on. */
    data class State(val kind: Kind, val referenceMillis: Long?, val silentForMillis: Long)

    fun state(nowMillis: Long, pairedAtMillis: Long?, lastHeartbeatMillis: Long?): State {
        val ref = lastHeartbeatMillis ?: pairedAtMillis ?: return State(Kind.UNPAIRED, null, 0)
        val silent = (nowMillis - ref).coerceAtLeast(0)
        val kind = when {
            silent > STALE_MILLIS -> Kind.STALE
            lastHeartbeatMillis == null -> Kind.WAITING
            else -> Kind.OK
        }
        return State(kind, ref, silent)
    }

    /** Whether the worker should post the "no heartbeat" alert now. */
    fun shouldAlert(nowMillis: Long, pairedAtMillis: Long?, lastHeartbeatMillis: Long?, lastAlertMillis: Long?): Boolean {
        if (state(nowMillis, pairedAtMillis, lastHeartbeatMillis).kind != Kind.STALE) return false
        return lastAlertMillis == null || nowMillis - lastAlertMillis >= REPEAT_MILLIS || nowMillis < lastAlertMillis
    }
}
