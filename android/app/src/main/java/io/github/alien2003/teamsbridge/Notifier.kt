package io.github.alien2003.teamsbridge

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import io.github.alien2003.teamsbridge.core.NotificationBudget
import io.github.alien2003.teamsbridge.proto.Message

/**
 * Local notifications (BRIEF §6). Two channels: `tbd.msg` (default importance) for Teams messages,
 * grouped by their Teams `tag`, and `tbd.alert` (high importance) for daemon alerts and the local
 * watchdog; alerts are ongoing until tapped. Platform `Notification.Builder` is enough at minSdk 28.
 */
object Notifier {
    const val CH_MSG = "tbd.msg"
    const val CH_ALERT = "tbd.alert"
    private const val GROUP_UNTAGGED = "tbd.msg"
    private const val ID_PAIRED = 1
    private const val ID_WATCHDOG = 2

    fun createChannels(ctx: Context) {
        val nm = ctx.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannels(
            listOf(
                NotificationChannel(CH_MSG, ctx.getString(R.string.channel_msg), NotificationManager.IMPORTANCE_DEFAULT).apply {
                    description = ctx.getString(R.string.channel_msg_desc)
                },
                NotificationChannel(CH_ALERT, ctx.getString(R.string.channel_alert), NotificationManager.IMPORTANCE_HIGH).apply {
                    description = ctx.getString(R.string.channel_alert_desc)
                },
            ),
        )
    }

    /** `msg`: one notification per message, grouped (with a summary) by Teams' tag. */
    fun showMessage(ctx: Context, m: Message) {
        val nm = ctx.getSystemService(NotificationManager::class.java)
        val group = if (m.tag.isEmpty()) GROUP_UNTAGGED else "$GROUP_UNTAGGED:${m.tag}"
        trim(nm, group)
        val id = notificationId("msg:${m.id}")
        val title = m.title.ifEmpty { ctx.getString(R.string.default_msg_title) }
        val n = Notification.Builder(ctx, CH_MSG)
            .setSmallIcon(R.drawable.ic_stat_tbd)
            .setContentTitle(title)
            .setContentText(m.body)
            .setStyle(Notification.BigTextStyle().bigText(m.body))
            .setCategory(Notification.CATEGORY_MESSAGE)
            .setWhen(m.ts * 1000)
            .setShowWhen(true)
            .setAutoCancel(true)
            .setGroup(group)
            .setContentIntent(OpenActivity.pendingIntent(ctx, m.url, id))
            .build()
        nm.notify(id, n)
        val summaryId = notificationId("group:$group")
        val summary = Notification.Builder(ctx, CH_MSG)
            .setSmallIcon(R.drawable.ic_stat_tbd)
            .setContentTitle(title)
            .setContentText(m.body)
            .setCategory(Notification.CATEGORY_MESSAGE)
            .setAutoCancel(true)
            .setGroup(group)
            .setGroupSummary(true)
            .setGroupAlertBehavior(Notification.GROUP_ALERT_CHILDREN)
            .setContentIntent(OpenActivity.pendingIntent(ctx, m.url, summaryId))
            .build()
        nm.notify(summaryId, summary)
    }

    /** `alert`: high importance, ongoing (not swipeable) until tapped; one per alert kind. */
    fun showAlert(ctx: Context, m: Message) {
        val id = notificationId("alert:${m.kind}")
        val title = m.title.ifEmpty {
            ctx.getString(
                when (m.kind) {
                    Message.KIND_SESSION_EXPIRED -> R.string.alert_session_expired
                    Message.KIND_BROWSER_DEAD -> R.string.alert_browser_dead
                    Message.KIND_DAEMON_STOPPING -> R.string.alert_daemon_stopping
                    else -> R.string.alert_generic
                },
            )
        }
        val n = alertBuilder(ctx, title, m.body)
            .setWhen(m.ts * 1000)
            .setContentIntent(OpenActivity.pendingIntent(ctx, m.url, id))
            .build()
        ctx.getSystemService(NotificationManager::class.java).notify(id, n)
    }

    /** The one-time "Paired ✓" after the first pair ping. Tapping opens the app. */
    fun showPaired(ctx: Context) {
        val n = Notification.Builder(ctx, CH_MSG)
            .setSmallIcon(R.drawable.ic_stat_tbd)
            .setContentTitle(ctx.getString(R.string.paired_title))
            .setContentText(ctx.getString(R.string.paired_text))
            .setCategory(Notification.CATEGORY_STATUS)
            .setAutoCancel(true)
            .setContentIntent(appIntent(ctx, ID_PAIRED))
            .build()
        ctx.getSystemService(NotificationManager::class.java).notify(ID_PAIRED, n)
    }

    /** The local watchdog alert (no heartbeat for more than 12 h). Tapping opens the Status screen. */
    fun showWatchdog(ctx: Context, silentHours: Long) {
        val n = alertBuilder(
            ctx,
            ctx.getString(R.string.watchdog_title),
            ctx.resources.getQuantityString(R.plurals.watchdog_text, silentHours.toInt(), silentHours.toInt()),
        ).setContentIntent(appIntent(ctx, ID_WATCHDOG)).build()
        ctx.getSystemService(NotificationManager::class.java).notify(ID_WATCHDOG, n)
    }

    fun cancelWatchdog(ctx: Context) = ctx.getSystemService(NotificationManager::class.java).cancel(ID_WATCHDOG)

    fun cancel(ctx: Context, id: Int) = ctx.getSystemService(NotificationManager::class.java).cancel(id)

    fun cancelAll(ctx: Context) = ctx.getSystemService(NotificationManager::class.java).cancelAll()

    /** Withdraws the oldest message notifications so a new one is never dropped by Android's cap. */
    private fun trim(nm: NotificationManager, group: String) {
        val active = nm.activeNotifications.map { sbn ->
            val n = sbn.notification
            NotificationBudget.Active(
                id = sbn.id,
                group = n.group,
                summary = n.flags and Notification.FLAG_GROUP_SUMMARY != 0,
                postTime = sbn.postTime,
                // Only our own untagged message notifications: cancel(id) reaches exactly those.
                trimmable = n.channelId == CH_MSG && sbn.tag == null && sbn.id != ID_PAIRED,
            )
        }
        NotificationBudget.toCancel(active, group).forEach(nm::cancel)
    }

    private fun alertBuilder(ctx: Context, title: String, text: String): Notification.Builder =
        Notification.Builder(ctx, CH_ALERT)
            .setSmallIcon(R.drawable.ic_stat_tbd)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(Notification.BigTextStyle().bigText(text))
            .setCategory(Notification.CATEGORY_ERROR)
            .setShowWhen(true)
            .setOngoing(true)
            .setAutoCancel(true)

    private fun appIntent(ctx: Context, requestCode: Int): PendingIntent = PendingIntent.getActivity(
        ctx,
        requestCode,
        Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    /** Stable ids from strings; small values are reserved for the fixed notifications above. */
    private fun notificationId(key: String): Int {
        val h = key.hashCode()
        return if (h in 0..15) h + 16 else h
    }
}
