package io.github.alien2003.teamsbridge

import android.content.Context
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.PeriodicWorkRequest
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import io.github.alien2003.teamsbridge.core.Watchdog
import java.util.concurrent.TimeUnit

/**
 * Hourly local watchdog (BRIEF §6): no heartbeat for more than 12 h → an alert on `tbd.alert`, at
 * most once per 12 h. Unique periodic work with KEEP, so re-scheduling on every app start is free.
 * Also retries a topic subscription that has not succeeded yet.
 */
class WatchdogWorker(ctx: Context, params: WorkerParameters) : Worker(ctx, params) {
    override fun doWork(): Result {
        val ctx = applicationContext
        val store = Store.get(ctx)
        if (!store.isPaired) return Result.success()
        if (!store.subscribed) Pairing.subscribe(ctx)
        val now = System.currentTimeMillis()
        if (Watchdog.shouldAlert(now, store.pairedAt, store.lastHeartbeatAt, store.watchdogAlertAt)) {
            val state = Watchdog.state(now, store.pairedAt, store.lastHeartbeatAt)
            Notifier.showWatchdog(ctx, TimeUnit.MILLISECONDS.toHours(state.silentForMillis))
            store.setWatchdogAlertAt(now)
        }
        return Result.success()
    }

    companion object {
        private const val NAME = "tbd-watchdog"

        fun schedule(ctx: Context) {
            val request = PeriodicWorkRequest.Builder(WatchdogWorker::class.java, Watchdog.CHECK_INTERVAL_HOURS, TimeUnit.HOURS).build()
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(NAME, ExistingPeriodicWorkPolicy.KEEP, request)
        }

        fun cancel(ctx: Context) {
            WorkManager.getInstance(ctx).cancelUniqueWork(NAME)
        }

        /** For the Status screen. */
        fun infos(ctx: Context) = WorkManager.getInstance(ctx).getWorkInfosForUniqueWorkLiveData(NAME)
    }
}
