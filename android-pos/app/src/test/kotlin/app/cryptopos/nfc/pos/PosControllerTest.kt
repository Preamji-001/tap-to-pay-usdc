package app.cryptopos.nfc.pos

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Runs every task when asked, so each step of the controller is visible to the test. */
private class ManualScheduler : Scheduler {
    val pending = ArrayDeque<() -> Unit>()
    val timers = mutableListOf<Pair<Long, () -> Unit>>()

    override fun background(task: () -> Unit) { pending.add(task) }
    override fun main(task: () -> Unit) { pending.add(task) }
    override fun after(delayMs: Long, task: () -> Unit): Cancellable {
        val t = delayMs to task
        timers.add(t)
        return Cancellable { timers.remove(t) }
    }

    fun runAll() { while (pending.isNotEmpty()) pending.removeFirst()() }

    /** Fires the poll timer and runs the work it starts. */
    fun firePoll() {
        val (_, task) = timers.removeFirst()
        task()
        runAll()
    }
}

private class FakeApi : CheckoutApi {
    var next: Checkout = checkout(CheckoutStatus.PENDING)
    var error: Exception? = null
    val created = mutableListOf<String>()
    var gets = 0

    override fun create(amount: String): Checkout {
        error?.let { throw it }
        created.add(amount)
        return next
    }

    override fun get(id: String): Checkout {
        gets++
        error?.let { throw it }
        return next
    }
}

private class FakeTag : TagServer {
    var url: String? = null
    var serves = 0
    override fun serve(url: String) { this.url = url; serves++ }
    override fun stop() { url = null }
}

private class MemStore : SaleStore {
    var id: String? = null
    override fun load() = id
    override fun save(id: String) { this.id = id }
    override fun clear() { id = null }
}

private fun checkout(status: CheckoutStatus, id: String = "co_AAAAAAAAAAAAAAAAAAAAAA") = Checkout(
    id = id,
    checkoutUrl = "http://192.168.1.20:5173/checkout/$id",
    status = status,
    amount = "12.5",
    symbol = "USDC",
    expiresAtMillis = 0,
    txHash = null,
)

class PosControllerTest {
    private val api = FakeApi()
    private val tag = FakeTag()
    private val store = MemStore()
    private val sched = ManualScheduler()
    private val states = mutableListOf<PosState>()
    private val pos = PosController(api, tag, store, sched, TagMode.METAMASK) { states.add(it) }

    private fun status() = (pos.state as PosState.Tracking).checkout.status

    @Test fun charge_createsServesAndPolls() {
        pos.charge(" 12.50 ")
        assertEquals(PosState.Creating("12.50"), pos.state)
        sched.runAll()

        assertEquals(listOf("12.50"), api.created)
        assertEquals("https://metamask.app.link/dapp/192.168.1.20:5173/checkout/co_AAAAAAAAAAAAAAAAAAAAAA", tag.url)
        assertEquals(CheckoutStatus.PENDING, status())
        assertEquals("co_AAAAAAAAAAAAAAAAAAAAAA", store.id)
        assertEquals(PosController.POLL_MS, sched.timers.single().first)
    }

    @Test fun fullSale_servesOnlyWhilePending_successOnlyAtConfirmed() {
        pos.charge("12.50")
        sched.runAll()

        sched.firePoll() // still pending: keep serving, don't re-create the tag mid-tap
        assertEquals(1, tag.serves)

        api.next = checkout(CheckoutStatus.SUBMITTING)
        sched.firePoll()
        assertNull("stop serving once the signature is in", tag.url)
        assertEquals(Tone.WAITING, screenFor(pos.state).tone)

        api.next = checkout(CheckoutStatus.SUBMITTED)
        sched.firePoll()
        assertEquals(Tone.WAITING, screenFor(pos.state).tone)

        api.next = checkout(CheckoutStatus.CONFIRMED)
        sched.firePoll()
        assertEquals(Tone.SUCCESS, screenFor(pos.state).tone)
        assertTrue("no more polling", sched.timers.isEmpty())
        assertNull("sale finished", store.id)

        assertEquals(1, states.count { screenFor(it).tone == Tone.SUCCESS })
    }

    @Test fun failedAndExpired_areFinalWithoutSuccess() {
        for (end in listOf(CheckoutStatus.FAILED, CheckoutStatus.EXPIRED)) {
            pos.newSale()
            pos.charge("1")
            sched.runAll()
            api.next = checkout(end)
            sched.firePoll()
            assertEquals(Tone.FAILURE, screenFor(pos.state).tone)
            assertNull(tag.url)
            assertTrue(sched.timers.isEmpty())
            api.next = checkout(CheckoutStatus.PENDING)
        }
    }

    @Test fun pollError_keepsStateAndRetries() {
        pos.charge("1")
        sched.runAll()
        api.error = ApiException(0, "unreachable", "down")
        sched.firePoll()
        val s = pos.state as PosState.Tracking
        assertEquals(CheckoutStatus.PENDING, s.checkout.status)
        assertTrue(s.reconnecting)
        assertEquals("https://metamask.app.link/dapp/192.168.1.20:5173/checkout/co_AAAAAAAAAAAAAAAAAAAAAA", tag.url)
        assertEquals(1, sched.timers.size)

        api.error = null
        sched.firePoll()
        assertTrue(!(pos.state as PosState.Tracking).reconnecting)
    }

    @Test fun createError_showsMessageAndAllowsRetry() {
        api.error = ApiException(401, "unauthorized", "missing or invalid API key")
        pos.charge("5")
        sched.runAll()
        val s = pos.state as PosState.CreateFailed
        assertEquals("The POS API key was rejected.", s.message)
        assertTrue(screenFor(s).amountEntry)
        assertNull(tag.url)

        api.error = null
        pos.charge("5")
        sched.runAll()
        assertEquals(CheckoutStatus.PENDING, status())
    }

    @Test fun badAmount_neverCallsTheServer() {
        for (bad in listOf("", "0", "0.000", "abc", "1.1234567", "-3", "1,50")) {
            pos.charge(bad)
            assertTrue(bad, pos.state is PosState.CreateFailed)
        }
        assertTrue(sched.pending.isEmpty())
    }

    @Test fun resume_picksUpSavedSale() {
        store.id = "co_AAAAAAAAAAAAAAAAAAAAAA"
        api.next = checkout(CheckoutStatus.SUBMITTED)
        pos.resume()
        assertEquals(PosState.Resuming("co_AAAAAAAAAAAAAAAAAAAAAA"), pos.state)
        sched.runAll()
        assertEquals(CheckoutStatus.SUBMITTED, status())
        assertNull("a submitted sale is not served again", tag.url)
        assertEquals(1, sched.timers.size)
    }

    @Test fun resume_pendingSale_servesAgain() {
        store.id = "co_AAAAAAAAAAAAAAAAAAAAAA"
        pos.resume()
        sched.runAll()
        assertEquals(1, tag.serves)
    }

    @Test fun resume_unknownSale_backToAmountEntry() {
        store.id = "co_gone"
        api.error = ApiException(404, "not_found", "checkout not found")
        pos.resume()
        sched.runAll()
        assertEquals(PosState.EnterAmount, pos.state)
        assertNull(store.id)
    }

    @Test fun cancel_stopsServingAndDropsLateAnswers() {
        pos.charge("1")
        sched.runAll()
        pos.newSale()
        assertNull(tag.url)
        assertNull(store.id)
        assertTrue(sched.timers.isEmpty())

        // A create that answers after the merchant cancelled must not come back to life.
        pos.charge("2")
        pos.newSale()
        sched.runAll()
        assertEquals(PosState.EnterAmount, pos.state)
        assertNull(tag.url)
    }

    @Test fun plainMode_servesCheckoutUrl() {
        val plain = PosController(api, tag, store, sched, TagMode.PLAIN) {}
        plain.charge("1")
        sched.runAll()
        assertEquals("http://192.168.1.20:5173/checkout/co_AAAAAAAAAAAAAAAAAAAAAA", tag.url)
    }
}
