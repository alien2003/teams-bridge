package io.github.alien2003.teamsbridge

import android.Manifest
import android.app.NotificationManager
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.view.View
import android.widget.Button
import android.widget.RadioGroup
import android.widget.TextView
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import com.google.android.material.appbar.MaterialToolbar
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.radiobutton.MaterialRadioButton
import io.github.alien2003.teamsbridge.proto.Proto

/** "Open Teams in:", notification permission and Unpair (BRIEF §6). */
class SettingsActivity : AppCompatActivity() {
    private lateinit var store: Store
    private lateinit var handlerGroup: RadioGroup
    private lateinit var notificationsStatus: TextView
    private lateinit var allowButton: Button
    private lateinit var pairingStatus: TextView
    private lateinit var unpairButton: Button

    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        // A second denial makes the system stop asking; the app's notification settings remain.
        if (!granted && !shouldShowRequestPermissionRationale(Manifest.permission.POST_NOTIFICATIONS)) openNotificationSettings()
        refreshNotifications()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_settings)
        findViewById<View>(R.id.root).padForSystemBars()
        val toolbar = findViewById<MaterialToolbar>(R.id.toolbar)
        setSupportActionBar(toolbar)
        supportActionBar?.setDisplayHomeAsUpEnabled(true)
        toolbar.setNavigationOnClickListener { finish() }
        store = Store.get(this)

        handlerGroup = findViewById(R.id.handlerGroup)
        notificationsStatus = findViewById(R.id.notificationsStatus)
        allowButton = findViewById(R.id.allowNotificationsButton)
        pairingStatus = findViewById(R.id.pairingStatus)
        unpairButton = findViewById(R.id.unpairButton)

        findViewById<Button>(R.id.testOpenButton).setOnClickListener { Launcher.open(this, Proto.DEFAULT_URL) }
        allowButton.setOnClickListener {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        findViewById<Button>(R.id.notificationSettingsButton).setOnClickListener { openNotificationSettings() }
        unpairButton.setOnClickListener { confirmUnpair() }
    }

    override fun onResume() {
        super.onResume()
        // Rebuilt every time: a browser or PWA may have been installed or removed meanwhile.
        buildHandlerList()
        refreshNotifications()
        refreshPairing()
    }

    private fun buildHandlerList() {
        handlerGroup.setOnCheckedChangeListener(null)
        handlerGroup.removeAllViews()
        val chosen = store.handlerPackage
        val handlers = Handlers.query(this)
        val iconSize = (32 * resources.displayMetrics.density).toInt()
        var checkedId = View.NO_ID

        fun add(text: CharSequence, pkg: String?, icon: android.graphics.drawable.Drawable?) {
            val rb = MaterialRadioButton(this)
            rb.id = View.generateViewId()
            rb.text = text
            rb.tag = pkg
            rb.minHeight = (48 * resources.displayMetrics.density).toInt()
            if (icon != null) {
                icon.setBounds(0, 0, iconSize, iconSize)
                rb.setCompoundDrawablesRelative(icon, null, null, null)
                rb.compoundDrawablePadding = (12 * resources.displayMetrics.density).toInt()
            }
            handlerGroup.addView(rb)
            if (pkg == chosen) checkedId = rb.id
        }

        add(getString(R.string.handler_system_default), null, null)
        for (h in handlers) {
            add(getString(R.string.handler_item, h.label, h.kind.label), h.packageName, h.info.loadIcon(packageManager))
        }
        if (chosen != null && handlers.none { it.packageName == chosen }) {
            add(getString(R.string.handler_unavailable, chosen), chosen, null)
        }
        findViewById<View>(R.id.noHandlers).visibility = if (handlers.isEmpty()) View.VISIBLE else View.GONE
        handlerGroup.check(checkedId)
        handlerGroup.setOnCheckedChangeListener { group, id ->
            store.handlerPackage = group.findViewById<View>(id)?.tag as String?
        }
    }

    private fun refreshNotifications() {
        val enabled = getSystemService(NotificationManager::class.java).areNotificationsEnabled()
        notificationsStatus.setText(if (enabled) R.string.notifications_allowed else R.string.notifications_blocked)
        val needsRuntime = Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        allowButton.visibility = if (needsRuntime) View.VISIBLE else View.GONE
    }

    private fun refreshPairing() {
        val paired = store.isPaired
        pairingStatus.setText(if (paired) R.string.pairing_paired else R.string.pairing_not_paired)
        unpairButton.visibility = if (paired) View.VISIBLE else View.GONE
    }

    private fun openNotificationSettings() {
        startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, packageName))
    }

    private fun confirmUnpair() {
        MaterialAlertDialogBuilder(this)
            .setTitle(R.string.unpair_confirm_title)
            .setMessage(R.string.unpair_confirm_text)
            .setNegativeButton(R.string.cancel, null)
            .setPositiveButton(R.string.unpair) { _, _ ->
                Pairing.unpair(this)
                refreshPairing()
                finish()
            }
            .show()
    }
}
