package app.cryptopos.nfc

import android.content.Context
import android.os.Handler
import android.os.Looper
import app.cryptopos.nfc.pos.Cancellable
import app.cryptopos.nfc.pos.SaleStore
import app.cryptopos.nfc.pos.Scheduler
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

/** Network calls on one background thread, results and timers on the main looper. */
class AndroidScheduler : Scheduler {
    private val io: ExecutorService = Executors.newSingleThreadExecutor()
    private val handler = Handler(Looper.getMainLooper())

    override fun background(task: () -> Unit) {
        io.execute(task)
    }

    override fun main(task: () -> Unit) {
        handler.post(task)
    }

    override fun after(delayMs: Long, task: () -> Unit): Cancellable {
        val r = Runnable(task)
        handler.postDelayed(r, delayMs)
        return Cancellable { handler.removeCallbacks(r) }
    }

    fun shutdown() {
        handler.removeCallbacksAndMessages(null)
        io.shutdownNow()
    }
}

/** The sale in progress, kept in SharedPreferences so a restart picks it up again. */
class PrefsSaleStore(context: Context) : SaleStore {
    private val prefs = context.getSharedPreferences("pos", Context.MODE_PRIVATE)

    override fun load(): String? = prefs.getString(KEY, null)

    override fun save(id: String) {
        prefs.edit().putString(KEY, id).apply()
    }

    override fun clear() {
        prefs.edit().remove(KEY).apply()
    }

    private companion object {
        const val KEY = "current_checkout_id"
    }
}
