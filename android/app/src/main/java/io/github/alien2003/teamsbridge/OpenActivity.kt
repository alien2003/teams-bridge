package io.github.alien2003.teamsbridge

import android.app.Activity
import android.app.PendingIntent
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.util.Log
import android.widget.Toast
import io.github.alien2003.teamsbridge.core.UrlPolicy

/**
 * The target of every notification tap that opens Teams. It has no UI (Theme.NoDisplay) and finishes
 * in onCreate.
 *
 * Why a trampoline instead of a PendingIntent that carries `ACTION_VIEW` + `setPackage(chosen)`: an
 * intent fired by the system from a notification cannot be caught, so the BRIEF's "on
 * ActivityNotFoundException fall back to the system chooser and show a toast" needs our own code to
 * run at tap time. It also reads the chosen handler at tap time, so changing the setting applies to
 * notifications already posted. Android 12's trampoline restriction applies to services and
 * broadcast receivers, not to activities.
 */
class OpenActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val notificationId = intent.getIntExtra(EXTRA_NOTIFICATION_ID, 0)
        if (notificationId != 0) Notifier.cancel(this, notificationId)
        Launcher.open(this, intent.dataString)
        finish()
    }

    companion object {
        private const val EXTRA_NOTIFICATION_ID = "notification_id"

        fun pendingIntent(ctx: Context, url: String, notificationId: Int): PendingIntent {
            val target = Intent(ctx, OpenActivity::class.java)
                .setData(Uri.parse(UrlPolicy.sanitize(url)))
                .putExtra(EXTRA_NOTIFICATION_ID, notificationId)
            return PendingIntent.getActivity(
                ctx,
                notificationId,
                target,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
        }
    }
}

/** Opens a Teams URL in the handler chosen under Settings → "Open Teams in:". */
object Launcher {
    fun open(ctx: Context, url: String?) {
        val uri = Uri.parse(UrlPolicy.sanitize(url))
        val pkg = Store.get(ctx).handlerPackage
        val view = Intent(Intent.ACTION_VIEW, uri)
            .addCategory(Intent.CATEGORY_BROWSABLE)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (pkg != null) view.setPackage(pkg)
        try {
            ctx.startActivity(view)
        } catch (_: ActivityNotFoundException) {
            Log.i(TAG, "chosen handler cannot open the link; falling back to the chooser")
            val app = ctx.applicationContext
            Toast.makeText(app, app.getString(R.string.handler_missing), Toast.LENGTH_LONG).show()
            val plain = Intent(Intent.ACTION_VIEW, uri).addCategory(Intent.CATEGORY_BROWSABLE)
            val chooser = Intent.createChooser(plain, app.getString(R.string.chooser_title))
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            try {
                ctx.startActivity(chooser)
            } catch (_: ActivityNotFoundException) {
                Toast.makeText(app, app.getString(R.string.no_handler), Toast.LENGTH_LONG).show()
            }
        }
    }
}
