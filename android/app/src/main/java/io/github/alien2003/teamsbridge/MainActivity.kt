package io.github.alien2003.teamsbridge

import android.Manifest
import android.app.NotificationManager
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.text.Editable
import android.text.TextWatcher
import android.util.Log
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputMethodManager
import android.widget.Button
import android.widget.TextView
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.work.WorkInfo
import com.google.android.material.appbar.MaterialToolbar
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.journeyapps.barcodescanner.ScanContract
import com.google.zxing.client.android.Intents
import com.journeyapps.barcodescanner.ScanOptions
import io.github.alien2003.teamsbridge.core.CodeMask
import io.github.alien2003.teamsbridge.core.Watchdog
import io.github.alien2003.teamsbridge.proto.Proto
import io.github.alien2003.teamsbridge.proto.Secret
import java.security.GeneralSecurityException
import java.security.ProviderException

/**
 * Pair screen (scan or type the code), then "waiting for pair ping", then the Status screen
 * (BRIEF §6). Which one shows follows the stored state; FCM updates arrive through the
 * SharedPreferences listener.
 */
class MainActivity : AppCompatActivity() {
    private lateinit var store: Store
    private lateinit var banner: TextView
    private lateinit var pairSection: View
    private lateinit var waitingSection: View
    private lateinit var statusSection: View
    private lateinit var codeLayout: TextInputLayout
    private lateinit var codeInput: TextInputEditText
    private lateinit var pairButton: Button
    private lateinit var waitingText: TextView
    private lateinit var waitingProgress: View
    private lateinit var retryButton: Button
    private lateinit var statusText: TextView
    private lateinit var notificationsWarning: TextView

    private var subscribing = false
    private var watchdogJob: WorkInfo.State? = null

    private val scan = registerForActivityResult(ScanContract()) { result ->
        val text = result.contents ?: return@registerForActivityResult
        val secret = Secret.parseOrNull(text)
        if (secret == null) {
            codeLayout.error = getString(R.string.pair_scan_not_tbd)
        } else {
            startPairing(secret)
        }
    }

    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) { refresh() }

    private val prefsListener = SharedPreferences.OnSharedPreferenceChangeListener { _, _ -> runOnUiThread { refresh() } }

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        findViewById<View>(R.id.root).padForSystemBars()
        setSupportActionBar(findViewById<MaterialToolbar>(R.id.toolbar))
        store = Store.get(this)

        banner = findViewById(R.id.placeholderBanner)
        pairSection = findViewById(R.id.pairSection)
        waitingSection = findViewById(R.id.waitingSection)
        statusSection = findViewById(R.id.statusSection)
        codeLayout = findViewById(R.id.codeLayout)
        codeInput = findViewById(R.id.codeInput)
        pairButton = findViewById(R.id.pairButton)
        waitingText = findViewById(R.id.waitingText)
        waitingProgress = findViewById(R.id.waitingProgress)
        retryButton = findViewById(R.id.retryButton)
        statusText = findViewById(R.id.statusText)
        notificationsWarning = findViewById(R.id.notificationsWarning)

        findViewById<Button>(R.id.scanButton).setOnClickListener { launchScanner() }
        pairButton.setOnClickListener { pairFromInput() }
        codeInput.addTextChangedListener(MaskWatcher {
            codeLayout.error = null
            pairButton.isEnabled = CodeMask.isComplete(codeInput.text ?: "")
        })
        codeInput.setOnEditorActionListener { _, actionId, _ ->
            if (actionId == EditorInfo.IME_ACTION_DONE) {
                pairFromInput()
                true
            } else {
                false
            }
        }
        retryButton.setOnClickListener { subscribe() }
        findViewById<Button>(R.id.cancelButton).setOnClickListener {
            Pairing.unpair(this)
            refresh()
        }
        findViewById<Button>(R.id.openTeamsButton).setOnClickListener { Launcher.open(this, Proto.DEFAULT_URL) }
        notificationsWarning.setOnClickListener { startActivity(Intent(this, SettingsActivity::class.java)) }
        WatchdogWorker.infos(this).observe(this) { infos ->
            watchdogJob = infos.firstOrNull { !it.state.isFinished }?.state ?: infos.firstOrNull()?.state
            refresh()
        }
    }

    override fun onStart() {
        super.onStart()
        store.registerListener(prefsListener)
        if (store.isPaired && !store.subscribed && !subscribing) subscribe()
        refresh()
    }

    override fun onStop() {
        store.unregisterListener(prefsListener)
        super.onStop()
    }

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        menuInflater.inflate(R.menu.main, menu)
        return true
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean = when (item.itemId) {
        R.id.action_settings -> {
            startActivity(Intent(this, SettingsActivity::class.java))
            true
        }
        else -> super.onOptionsItemSelected(item)
    }

    private fun launchScanner() {
        val options = ScanOptions()
            .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            .setPrompt(getString(R.string.pair_scan_prompt))
            .setBeepEnabled(false)
            .setOrientationLocked(false)
            // `tbd pair` draws light modules on a dark terminal (an inverted code) unless --invert:
            // decode both polarities.
            .addExtra(Intents.Scan.SCAN_TYPE, Intents.Scan.MIXED_SCAN)
        scan.launch(options)
    }

    private fun pairFromInput() {
        val secret = Secret.parseOrNull(codeInput.text?.toString().orEmpty())
        if (secret == null) {
            codeLayout.error = getString(R.string.pair_invalid)
            return
        }
        startPairing(secret)
    }

    private fun startPairing(secret: Secret) {
        getSystemService(InputMethodManager::class.java)?.hideSoftInputFromWindow(codeInput.windowToken, 0)
        subscribing = true
        try {
            Pairing.pair(this, secret) { onSubscribed() }
        } catch (e: GeneralSecurityException) {
            pairingFailed(e)
            return
        } catch (e: ProviderException) {
            pairingFailed(e)
            return
        }
        codeInput.text?.clear()
        codeLayout.error = null
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        refresh()
    }

    private fun pairingFailed(e: Exception) {
        Log.w(TAG, "cannot store the pairing secret: ${e.javaClass.simpleName}")
        subscribing = false
        codeLayout.error = getString(R.string.pair_store_failed)
    }

    private fun subscribe() {
        subscribing = true
        refresh()
        Pairing.subscribe(this) { onSubscribed() }
    }

    private fun onSubscribed() {
        subscribing = false
        refresh()
    }

    private fun refresh() {
        if (isFinishing || isDestroyed) return
        val placeholder = BuildInfo.placeholderFirebase(this)
        banner.visibility = if (placeholder) View.VISIBLE else View.GONE
        val paired = store.isPaired
        val contact = paired && store.hasContact
        pairSection.visibility = if (!paired) View.VISIBLE else View.GONE
        waitingSection.visibility = if (paired && !contact) View.VISIBLE else View.GONE
        statusSection.visibility = if (contact) View.VISIBLE else View.GONE
        if (!paired) {
            pairButton.isEnabled = CodeMask.isComplete(codeInput.text ?: "")
            return
        }
        if (!contact) {
            val failed = !subscribing && !store.subscribed
            waitingText.setText(
                when {
                    subscribing -> R.string.waiting_subscribing
                    failed && placeholder -> R.string.waiting_placeholder
                    failed -> R.string.waiting_subscribe_failed
                    else -> R.string.waiting_text
                },
            )
            waitingProgress.visibility = if (failed) View.GONE else View.VISIBLE
            retryButton.visibility = if (failed && !placeholder) View.VISIBLE else View.GONE
            return
        }
        statusText.text = statusLines()
        val nm = getSystemService(NotificationManager::class.java)
        notificationsWarning.visibility = if (nm.areNotificationsEnabled()) View.GONE else View.VISIBLE
    }

    private fun statusLines(): String {
        val now = System.currentTimeMillis()
        val wd = Watchdog.state(now, store.pairedAt, store.lastHeartbeatAt)
        val watchdog = when (wd.kind) {
            Watchdog.Kind.UNPAIRED -> getString(R.string.watchdog_unpaired)
            Watchdog.Kind.WAITING -> getString(R.string.watchdog_waiting, duration(wd.silentForMillis))
            Watchdog.Kind.OK -> getString(R.string.watchdog_ok)
            Watchdog.Kind.STALE -> getString(R.string.watchdog_stale, duration(wd.silentForMillis))
        }
        val job = when (watchdogJob) {
            WorkInfo.State.ENQUEUED, WorkInfo.State.RUNNING, WorkInfo.State.BLOCKED -> getString(R.string.watchdog_job_scheduled)
            null -> getString(R.string.watchdog_job_unknown)
            else -> getString(R.string.watchdog_job_stopped)
        }
        return listOf(
            getString(R.string.status_paired, ago(store.pairedAt, now)),
            getString(if (store.subscribed) R.string.status_subscribed else R.string.status_not_subscribed),
            getString(R.string.status_heartbeat, ago(store.lastHeartbeatAt, now)),
            getString(R.string.status_message, ago(store.lastMessageAt, now)),
            getString(R.string.status_alert, ago(store.lastAlertAt, now)),
            "$watchdog $job",
        ).joinToString("\n")
    }

    /** Applies [CodeMask] while the user types or pastes. */
    private class MaskWatcher(private val changed: () -> Unit) : TextWatcher {
        private var busy = false

        override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit

        override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) = Unit

        override fun afterTextChanged(s: Editable) {
            if (busy) return
            val formatted = CodeMask.format(s)
            if (formatted != s.toString()) {
                busy = true
                s.replace(0, s.length, formatted)
                busy = false
            }
            changed()
        }
    }
}
