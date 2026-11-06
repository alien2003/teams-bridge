package io.github.alien2003.teamsbridge

import android.content.Context
import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import io.github.alien2003.teamsbridge.proto.Envelope
import io.github.alien2003.teamsbridge.proto.Message
import io.github.alien2003.teamsbridge.proto.ProtoException
import io.github.alien2003.teamsbridge.proto.ReplayFilter

/**
 * Receives the daemon's FCM data messages (BRIEF §6). Everything that fails decryption or the
 * receiver rules is dropped silently: debug log with a fixed reason, never content.
 */
class MessagingService : FirebaseMessagingService() {
    override fun onMessageReceived(message: RemoteMessage) {
        val c = message.data["c"]
        if (c == null) {
            Log.d(TAG, "dropped: data message without c")
            return
        }
        Receiver.handle(applicationContext, c, System.currentTimeMillis())
    }

    // A new registration has no topic subscriptions: renew ours if paired. Identifiers are not logged.
    // FCM 25.1 deprecated token registration (onNewToken) in favour of opt-in registration by
    // Firebase installation ID (onRegistered); tokens remain the SDK default, so both are handled.

    @Suppress("OVERRIDE_DEPRECATION")
    override fun onNewToken(token: String) = renewSubscription()

    override fun onRegistered(installationId: String) = renewSubscription()

    private fun renewSubscription() {
        if (Store.get(this).isPaired) Pairing.subscribe(applicationContext)
    }
}

/** Decrypt → receiver rules → dispatch on `t`. */
object Receiver {
    private val lock = Any()

    fun handle(ctx: Context, c: String, nowMillis: Long) {
        val store = Store.get(ctx)
        val secret = store.secret()
        if (secret == null) {
            Log.d(TAG, if (store.isPaired) "dropped: pairing secret unavailable" else "dropped: not paired")
            return
        }
        val m = try {
            Envelope.open(secret.key(), c)
        } catch (e: ProtoException) {
            Log.d(TAG, "dropped: ${e.message}")
            return
        }
        val accepted = synchronized(lock) {
            val filter = ReplayFilter(store.replayIds())
            filter.accept(m, nowMillis).also { if (it) store.saveReplayIds(filter.snapshot()) }
        }
        if (!accepted) {
            Log.d(TAG, "dropped: ${m.t} too old or duplicate")
            return
        }
        Log.d(TAG, "accepted ${m.t}")
        // Anything that arrived proves the topic subscription works.
        if (!store.subscribed) store.subscribed = true
        when (m.t) {
            Message.MSG -> {
                store.setLastMessageAt(nowMillis)
                Notifier.showMessage(ctx, m)
            }
            Message.ALERT -> {
                store.setLastAlertAt(nowMillis)
                Notifier.showAlert(ctx, m)
            }
            Message.HB -> {
                store.setLastHeartbeatAt(nowMillis)
                Notifier.cancelWatchdog(ctx)
            }
            Message.PAIR -> {
                val first = store.pairPingAt == null
                store.setPairPingAt(nowMillis)
                if (first) Notifier.showPaired(ctx)
            }
        }
    }
}
