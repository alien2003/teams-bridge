package io.github.alien2003.teamsbridge.core

/**
 * Android silently drops every new notification of an app that already shows 50
 * (NotificationManagerService.MAX_PACKAGE_NOTIFICATIONS). One notification per Teams message plus a
 * summary per chat reaches that after a day of unread chats on the phone, and from then on new
 * messages would vanish. Before a message is posted, the oldest message notifications are therefore
 * withdrawn so that at most [LIMIT] remain. Alerts and the app's own notices are never withdrawn.
 */
object NotificationBudget {
    /** Active notifications allowed after posting; headroom below Android's 50 for alerts and races. */
    const val LIMIT = 40

    /** One active notification of the app. [trimmable]: a Teams message or a message group summary. */
    data class Active(val id: Int, val group: String?, val summary: Boolean, val postTime: Long, val trimmable: Boolean)

    /**
     * The ids to cancel before posting one message into [group] (plus that group's summary unless it
     * is already showing) so that at most [limit] notifications remain. Summaries left without
     * children go first, then the oldest messages; a summary goes with its last child, except the
     * summary of [group] itself.
     */
    fun toCancel(active: List<Active>, group: String, limit: Int = LIMIT): List<Int> {
        val incoming = if (active.any { it.summary && it.group == group }) 1 else 2
        var count = active.size
        if (count + incoming <= limit) return emptyList()
        val children = active.filter { it.trimmable && !it.summary }.sortedBy { it.postTime }
        val summaries = active.filter { it.trimmable && it.summary }.sortedBy { it.postTime }
        val left = children.groupingBy { it.group }.eachCount().toMutableMap()
        val out = ArrayList<Int>()
        for (s in summaries) {
            if (count + incoming <= limit) return out
            if (s.group != group && (left[s.group] ?: 0) == 0) {
                out += s.id
                count--
            }
        }
        for (c in children) {
            if (count + incoming <= limit) break
            out += c.id
            count--
            val n = left.getValue(c.group) - 1
            left[c.group] = n
            if (n == 0 && c.group != group) {
                summaries.firstOrNull { it.group == c.group }?.let {
                    out += it.id
                    count--
                }
            }
        }
        return out
    }
}
