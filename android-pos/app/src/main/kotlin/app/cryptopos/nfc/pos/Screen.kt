package app.cryptopos.nfc.pos

enum class Tone { NEUTRAL, WAITING, SUCCESS, FAILURE }

enum class Action { NEW_SALE, CANCEL_SALE }

/** What the POS shows for a state. Success appears only for a confirmed checkout. */
data class Screen(
    val title: String,
    val detail: String,
    val tone: Tone,
    val amountEntry: Boolean = false,
    val action: Action? = null,
    val countdown: Boolean = false,
)

fun screenFor(state: PosState): Screen = when (state) {
    PosState.EnterAmount -> Screen("New sale", "Enter the amount in USDC.", Tone.NEUTRAL, amountEntry = true)
    is PosState.Creating -> Screen("Creating checkout…", "${state.amount} USDC", Tone.WAITING)
    is PosState.CreateFailed -> Screen("Could not create checkout", state.message, Tone.FAILURE, amountEntry = true)
    is PosState.Resuming -> Screen("Reconnecting…", "Loading the sale in progress.", Tone.WAITING)
    is PosState.Tracking -> tracking(state)
}

private fun tracking(state: PosState.Tracking): Screen {
    val c = state.checkout
    val due = "${c.amount} ${c.symbol}"
    val base = when (c.status) {
        CheckoutStatus.PENDING -> Screen(
            "Tap to pay $due", "Hold the top of the customer's phone to the back of this phone.",
            Tone.WAITING, action = Action.CANCEL_SALE, countdown = true,
        )
        CheckoutStatus.SUBMITTING -> Screen("Signature received", "Sending $due…", Tone.WAITING)
        CheckoutStatus.SUBMITTED -> Screen("Processing $due", "Waiting for the network to confirm.", Tone.WAITING)
        CheckoutStatus.CONFIRMED -> Screen("Paid $due", "Payment confirmed on chain.", Tone.SUCCESS, action = Action.NEW_SALE)
        CheckoutStatus.FAILED -> Screen("Payment failed", "Nothing was paid. Start a new sale.", Tone.FAILURE, action = Action.NEW_SALE)
        CheckoutStatus.EXPIRED -> Screen("Checkout expired", "No payment arrived in time.", Tone.FAILURE, action = Action.NEW_SALE)
    }
    return if (state.reconnecting) base.copy(detail = base.detail + "\nReconnecting to the server…") else base
}
