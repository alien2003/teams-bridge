package io.github.alien2003.teamsbridge

import android.content.Context
import android.text.format.DateUtils
import android.view.View
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat

/** Edge-to-edge (enforced from targetSdk 35): pad the root by the system bars, cutout and keyboard. */
internal fun View.padForSystemBars() {
    ViewCompat.setOnApplyWindowInsetsListener(this) { v, insets ->
        val types = WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout() or WindowInsetsCompat.Type.ime()
        val b = insets.getInsets(types)
        v.setPadding(b.left, b.top, b.right, b.bottom)
        WindowInsetsCompat.CONSUMED
    }
}

/** "5 minutes ago", or "never". */
internal fun Context.ago(millis: Long?, now: Long = System.currentTimeMillis()): CharSequence =
    if (millis == null) getString(R.string.never) else DateUtils.getRelativeTimeSpanString(millis, now, DateUtils.MINUTE_IN_MILLIS)

/** "3 hours", "2 days": a duration for the watchdog line. */
internal fun Context.duration(millis: Long): String {
    val minutes = millis / DateUtils.MINUTE_IN_MILLIS
    val hours = millis / DateUtils.HOUR_IN_MILLIS
    val days = millis / DateUtils.DAY_IN_MILLIS
    return when {
        days >= 2 -> resources.getQuantityString(R.plurals.duration_days, days.toInt(), days.toInt())
        hours >= 1 -> resources.getQuantityString(R.plurals.duration_hours, hours.toInt(), hours.toInt())
        else -> resources.getQuantityString(R.plurals.duration_minutes, minutes.toInt(), minutes.toInt())
    }
}
