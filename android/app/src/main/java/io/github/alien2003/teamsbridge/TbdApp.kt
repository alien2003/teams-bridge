package io.github.alien2003.teamsbridge

import android.app.Application
import com.google.android.material.color.DynamicColors

class TbdApp : Application() {
    override fun onCreate() {
        super.onCreate()
        DynamicColors.applyToActivitiesIfAvailable(this)
        Notifier.createChannels(this)
        if (Store.get(this).isPaired) WatchdogWorker.schedule(this)
    }
}
