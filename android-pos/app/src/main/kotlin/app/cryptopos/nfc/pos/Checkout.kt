package app.cryptopos.nfc.pos

/** The backend's checkout states (lowercase on the wire). */
enum class CheckoutStatus {
    PENDING, SUBMITTING, SUBMITTED, CONFIRMED, FAILED, EXPIRED;

    val terminal: Boolean get() = this == CONFIRMED || this == FAILED || this == EXPIRED

    companion object {
        fun parse(wire: String): CheckoutStatus =
            entries.firstOrNull { it.name.lowercase() == wire }
                ?: throw ApiException(0, "bad_response", "unknown checkout status \"$wire\"")
    }
}

/** The fields of a backend checkout the POS shows or acts on. */
data class Checkout(
    val id: String,
    val checkoutUrl: String,
    val status: CheckoutStatus,
    val amount: String,
    val symbol: String,
    val expiresAtMillis: Long,
    val txHash: String?,
)

/** A failed API call. [status] is the HTTP status, 0 when the server was never reached. */
class ApiException(val status: Int, val code: String, message: String) : Exception(message)

interface CheckoutApi {
    fun create(amount: String): Checkout
    fun get(id: String): Checkout
}
