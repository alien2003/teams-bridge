package io.github.alien2003.teamsbridge

import android.content.Context
import android.util.Log
import com.google.firebase.messaging.FirebaseMessaging
import io.github.alien2003.teamsbridge.proto.Secret

/**
 * Pairing lifecycle: store S, subscribe to its topic, schedule the watchdog; and the reverse.
 * FCM's subscribe/unsubscribe tasks complete on the main thread.
 */
object Pairing {

    /** Stores [secret] as the new pairing and subscribes to its topic; [done] reports the subscription. */
    fun pair(ctx: Context, secret: Secret, done: (Boolean) -> Unit) {
        val app = ctx.applicationContext
        val store = Store.get(app)
        val old = store.secret()?.topic()
        // May throw (Keystore): the previous pairing then stays as it was, still subscribed.
        store.pair(secret, System.currentTimeMillis())
        // Re-pairing over an existing pairing: stop receiving the old topic (its key is replaced).
        if (old != null && old != secret.topic() && !BuildInfo.placeholderFirebase(app)) {
            FirebaseMessaging.getInstance().unsubscribeFromTopic(old)
        }
        Notifier.cancelAll(app)
        WatchdogWorker.schedule(app)
        subscribe(app, done)
    }

    /** (Re-)subscribes to the stored pairing's topic. Safe to call repeatedly. */
    fun subscribe(ctx: Context, done: (Boolean) -> Unit = {}) {
        val store = Store.get(ctx)
        val topic = store.secret()?.topic() ?: return done(false)
        if (BuildInfo.placeholderFirebase(ctx)) {
            Log.w(TAG, "placeholder google-services.json: not subscribing")
            store.subscribed = false
            return done(false)
        }
        FirebaseMessaging.getInstance().subscribeToTopic(topic).addOnCompleteListener { task ->
            val ok = task.isSuccessful
            // The topic is secret-derived: log the outcome only.
            if (ok) {
                Log.i(TAG, "subscribed to the pairing topic")
            } else {
                Log.w(TAG, "topic subscription failed: ${task.exception?.javaClass?.simpleName}")
            }
            if (store.secret()?.topic() == topic) store.subscribed = ok
            done(ok)
        }
    }

    /** Unsubscribes (best effort, also when offline: the key is gone either way) and wipes the pairing. */
    fun unpair(ctx: Context) {
        val app = ctx.applicationContext
        val store = Store.get(app)
        val topic = store.secret()?.topic()
        if (topic != null && !BuildInfo.placeholderFirebase(app)) {
            FirebaseMessaging.getInstance().unsubscribeFromTopic(topic).addOnCompleteListener { task ->
                if (!task.isSuccessful) Log.w(TAG, "topic unsubscription failed; messages to it can no longer be decrypted")
            }
        }
        store.unpair()
        WatchdogWorker.cancel(app)
        Notifier.cancelAll(app)
    }
}

/** Build-time facts the UI shows. */
object BuildInfo {
    /** True when the APK was built with android/app/google-services.json.example (FCM cannot work). */
    fun placeholderFirebase(ctx: Context): Boolean = ctx.getString(R.string.project_id) == PLACEHOLDER_PROJECT_ID

    private const val PLACEHOLDER_PROJECT_ID = "tbd-placeholder"
}
