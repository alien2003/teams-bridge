package io.github.alien2003.teamsbridge.proto

/**
 * Receiver rules 3 and 4 (PROTOCOL.md §7): drop a message whose `ts` is more than 24 h in the past
 * (exactly 24 h is accepted) and drop any id among the last 256 accepted ones. Mirrors Go's
 * `proto.Filter`. Not thread-safe; the app serialises access and persists [snapshot].
 */
class ReplayFilter(initial: List<String> = emptyList()) {
    // Insertion-ordered: the first element is the oldest remembered id.
    private val ids = LinkedHashSet<String>()

    init {
        // Keep the newest REPLAY_SIZE distinct ids of a stored list, oldest first. Index loops on
        // purpose: JDK 21's SequencedCollection.reversed() members would shadow Kotlin's reversed()
        // and do not exist on older Android.
        val newest = ArrayList<String>(Proto.REPLAY_SIZE)
        val seen = HashSet<String>()
        for (i in initial.indices.reversed()) {
            if (newest.size == Proto.REPLAY_SIZE) break
            if (seen.add(initial[i])) newest.add(initial[i])
        }
        for (i in newest.indices.reversed()) ids.add(newest[i])
    }

    /** Whether [m] should be shown at [nowMillis]; remembers its id if so. */
    fun accept(m: Message, nowMillis: Long): Boolean = accept(m.id, m.ts, nowMillis)

    fun accept(id: String, ts: Long, nowMillis: Long): Boolean {
        if (isTooOld(ts, nowMillis)) return false
        if (id in ids) return false
        if (ids.size >= Proto.REPLAY_SIZE) {
            val oldest = ids.iterator()
            oldest.next()
            oldest.remove()
        }
        ids.add(id)
        return true
    }

    /** The remembered ids, oldest first. */
    fun snapshot(): List<String> = ids.toList()

    companion object {
        /** `now - ts > 24 h`, without overflowing for absurd `ts` values (Go saturates the same way). */
        fun isTooOld(ts: Long, nowMillis: Long): Boolean {
            val tsMillis = when {
                ts > Long.MAX_VALUE / 1000 -> Long.MAX_VALUE
                ts < Long.MIN_VALUE / 1000 -> Long.MIN_VALUE
                else -> ts * 1000
            }
            return tsMillis < nowMillis - Proto.MAX_AGE_MILLIS
        }
    }
}
