package app.cryptopos.nfc

import android.app.Activity
import android.content.Intent
import android.nfc.NfcAdapter
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.View
import android.view.inputmethod.EditorInfo
import android.widget.Button
import android.widget.EditText
import android.widget.ScrollView
import android.widget.TextView
import app.cryptopos.nfc.pos.Action
import app.cryptopos.nfc.pos.HttpCheckoutApi
import app.cryptopos.nfc.pos.PosController
import app.cryptopos.nfc.pos.PosState
import app.cryptopos.nfc.pos.TagMode
import app.cryptopos.nfc.pos.Tone
import app.cryptopos.nfc.pos.screenFor

/**
 * The POS screen: amount entry, then the sale's status until it is final.
 * The N1 APDU log stays available as a debug panel.
 */
class MainActivity : Activity() {
    private lateinit var title: TextView
    private lateinit var detail: TextView
    private lateinit var countdown: TextView
    private lateinit var amountRow: View
    private lateinit var amountInput: EditText
    private lateinit var action: Button
    private lateinit var nfcState: TextView
    private lateinit var nfcSettings: Button
    private lateinit var logToggle: Button
    private lateinit var log: TextView
    private lateinit var logScroll: ScrollView

    private lateinit var scheduler: AndroidScheduler
    private lateinit var controller: PosController
    private val ticker = Handler(Looper.getMainLooper())
    private val tick = object : Runnable {
        override fun run() {
            renderCountdown()
            ticker.postDelayed(this, 1_000)
        }
    }
    private val onLog: () -> Unit = { runOnUiThread(::renderLog) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        title = findViewById(R.id.status_title)
        detail = findViewById(R.id.status_detail)
        countdown = findViewById(R.id.countdown)
        amountRow = findViewById(R.id.amount_row)
        amountInput = findViewById(R.id.amount)
        action = findViewById(R.id.action)
        nfcState = findViewById(R.id.nfc_state)
        nfcSettings = findViewById(R.id.nfc_settings)
        logToggle = findViewById(R.id.log_toggle)
        log = findViewById(R.id.log)
        logScroll = findViewById(R.id.log_scroll)

        scheduler = AndroidScheduler()
        controller = PosController(
            api = HttpCheckoutApi(BuildConfig.API_BASE_URL, BuildConfig.API_KEY),
            tag = TagHolder,
            store = PrefsSaleStore(this),
            scheduler = scheduler,
            tagMode = if (BuildConfig.TAG_MODE == "plain") TagMode.PLAIN else TagMode.METAMASK,
            onChange = ::render,
        )

        findViewById<Button>(R.id.charge).setOnClickListener { charge() }
        amountInput.setOnEditorActionListener { _, id, _ -> (id == EditorInfo.IME_ACTION_DONE).also { if (it) charge() } }
        action.setOnClickListener { controller.newSale() }
        nfcSettings.setOnClickListener { startActivity(Intent(Settings.ACTION_NFC_SETTINGS)) }
        logToggle.setOnClickListener {
            logScroll.visibility = if (logScroll.visibility == View.VISIBLE) View.GONE else View.VISIBLE
        }
        findViewById<TextView>(R.id.server).text = getString(R.string.server_label, BuildConfig.API_BASE_URL, BuildConfig.TAG_MODE)

        render(controller.state)
        controller.resume()
    }

    override fun onResume() {
        super.onResume()
        renderNfcState()
        ApduLog.addListener(onLog)
        renderLog()
        ticker.post(tick)
    }

    override fun onPause() {
        ApduLog.removeListener(onLog)
        ticker.removeCallbacks(tick)
        super.onPause()
    }

    override fun onDestroy() {
        controller.close()
        scheduler.shutdown()
        super.onDestroy()
    }

    private fun charge() {
        controller.charge(amountInput.text.toString())
    }

    private fun render(state: PosState) {
        val s = screenFor(state)
        title.text = s.title
        title.setTextColor(getColor(when (s.tone) {
            Tone.SUCCESS -> R.color.success
            Tone.FAILURE -> R.color.failure
            Tone.WAITING -> R.color.waiting
            Tone.NEUTRAL -> R.color.neutral
        }))
        detail.text = s.detail
        amountRow.visibility = if (s.amountEntry) View.VISIBLE else View.GONE
        action.visibility = if (s.action != null) View.VISIBLE else View.GONE
        action.setText(if (s.action == Action.CANCEL_SALE) R.string.cancel_sale else R.string.new_sale)
        countdown.visibility = if (s.countdown) View.VISIBLE else View.GONE
        if (state is PosState.EnterAmount) amountInput.text.clear()
        renderCountdown()
    }

    private fun renderCountdown() {
        val c = (controller.state as? PosState.Tracking)?.checkout ?: return
        val left = ((c.expiresAtMillis - System.currentTimeMillis()) / 1000).coerceAtLeast(0)
        countdown.text = getString(R.string.expires_in, left / 60, left % 60)
    }

    private fun renderNfcState() {
        val adapter = NfcAdapter.getDefaultAdapter(this)
        val (text, showSettings) = when {
            adapter == null -> getString(R.string.nfc_none) to false
            !adapter.isEnabled -> getString(R.string.nfc_off) to true
            else -> getString(R.string.nfc_on) to false
        }
        nfcState.text = text
        nfcSettings.visibility = if (showSettings) View.VISIBLE else View.GONE
    }

    private fun renderLog() {
        log.text = ApduLog.lines().joinToString("\n")
        logScroll.post { logScroll.fullScroll(View.FOCUS_DOWN) }
    }
}
