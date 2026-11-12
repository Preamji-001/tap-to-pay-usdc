package app.cryptopos.nfc.pos

/** Runs work off the main thread and delivers results back on it. */
interface Scheduler {
    fun background(task: () -> Unit)
    fun main(task: () -> Unit)
    fun after(delayMs: Long, task: () -> Unit): Cancellable
}

fun interface Cancellable {
    fun cancel()
}

/** Whatever the HCE service should answer taps with. */
interface TagServer {
    fun serve(url: String)
    fun stop()
}

/** Remembers the sale in progress across app restarts. */
interface SaleStore {
    fun load(): String?
    fun save(id: String)
    fun clear()
}

sealed interface PosState {
    data object EnterAmount : PosState
    data class Creating(val amount: String) : PosState
    data class CreateFailed(val amount: String, val message: String) : PosState
    data class Resuming(val id: String) : PosState
    data class Tracking(val checkout: Checkout, val reconnecting: Boolean = false) : PosState
}

/**
 * One sale at a time: create the checkout, serve its link while it is pending, poll until it is final.
 * All state changes happen on the main thread (via [Scheduler.main]).
 */
class PosController(
    private val api: CheckoutApi,
    private val tag: TagServer,
    private val store: SaleStore,
    private val scheduler: Scheduler,
    private val tagMode: TagMode,
    private val onChange: (PosState) -> Unit,
) {
    var state: PosState = PosState.EnterAmount
        private set(value) {
            field = value
            onChange(value)
        }

    // Each sale gets a generation; answers that arrive for an older sale are dropped.
    private var generation = 0
    private var servedId: String? = null
    private var poll: Cancellable? = null

    /** Picks the saved sale back up, e.g. after the app was killed mid-sale. */
    fun resume() {
        val id = store.load() ?: return
        val gen = ++generation
        state = PosState.Resuming(id)
        fetch(gen, id)
    }

    fun charge(amount: String) {
        val trimmed = amount.trim()
        if (!AMOUNT.matches(trimmed) || trimmed.toBigDecimal().signum() <= 0) {
            state = PosState.CreateFailed(trimmed, "Enter an amount like 12.50 (at most 6 decimals).")
            return
        }
        val gen = ++generation
        state = PosState.Creating(trimmed)
        scheduler.background {
            val result = runCatching { api.create(trimmed) }
            scheduler.main {
                if (gen != generation) return@main
                result.fold(
                    onSuccess = { store.save(it.id); apply(gen, it) },
                    onFailure = { state = PosState.CreateFailed(trimmed, describe(it)) },
                )
            }
        }
    }

    /** Back to amount entry. A pending checkout stops being served and simply expires on the backend. */
    fun newSale() {
        generation++
        stopPolling()
        stopServing()
        store.clear()
        state = PosState.EnterAmount
    }

    /** Stops polling when the screen goes away; the saved sale is resumed later. */
    fun close() {
        generation++
        stopPolling()
    }

    private fun fetch(gen: Int, id: String) {
        scheduler.background {
            val result = runCatching { api.get(id) }
            scheduler.main {
                if (gen != generation) return@main
                result.fold(
                    onSuccess = { apply(gen, it) },
                    onFailure = { err ->
                        if (err is ApiException && err.status == 404) {
                            newSale() // the backend no longer knows this sale
                        } else {
                            (state as? PosState.Tracking)?.let { state = it.copy(reconnecting = true) }
                            schedulePoll(gen, id)
                        }
                    },
                )
            }
        }
    }

    private fun apply(gen: Int, c: Checkout) {
        // Serve only while a signature is still wanted; a tap after that would be pointless.
        if (c.status == CheckoutStatus.PENDING) {
            if (servedId != c.id) {
                tag.serve(tagUrl(c.checkoutUrl, tagMode))
                servedId = c.id
            }
        } else {
            stopServing()
        }
        state = PosState.Tracking(c)
        if (c.status.terminal) {
            store.clear()
            stopPolling()
        } else {
            schedulePoll(gen, c.id)
        }
    }

    private fun schedulePoll(gen: Int, id: String) {
        poll?.cancel()
        poll = scheduler.after(POLL_MS) { if (gen == generation) fetch(gen, id) }
    }

    private fun stopPolling() {
        poll?.cancel()
        poll = null
    }

    private fun stopServing() {
        if (servedId != null) tag.stop()
        servedId = null
    }

    companion object {
        const val POLL_MS = 2_000L
        private val AMOUNT = Regex("""\d{1,12}(\.\d{1,6})?""")

        fun describe(err: Throwable): String = when {
            err !is ApiException -> err.message ?: "unexpected error"
            err.status == 0 -> "Cannot reach the payment server. Check Wi-Fi and the server address."
            err.code == "unauthorized" -> "The POS API key was rejected."
            err.code == "chain_unavailable" -> "The payment network is unreachable. Try again."
            else -> err.message ?: err.code
        }
    }
}
