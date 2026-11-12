package app.cryptopos.nfc.pos

import com.sun.net.httpserver.HttpServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.fail
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.net.InetSocketAddress

/** Against a real local HTTP server; Robolectric provides Android's org.json. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [30])
class HttpCheckoutApiTest {
    private val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0).apply { start() }
    private val base = "http://127.0.0.1:${server.address.port}"
    private var seen: Map<String, String?> = emptyMap()

    @After fun stop() = server.stop(0)

    private fun respond(path: String, status: Int, body: String) {
        server.createContext(path) { ex ->
            seen = mapOf(
                "method" to ex.requestMethod,
                "path" to ex.requestURI.path,
                "key" to ex.requestHeaders.getFirst("X-API-Key"),
                "type" to ex.requestHeaders.getFirst("Content-Type"),
                "body" to ex.requestBody.readBytes().decodeToString(),
            )
            val bytes = body.toByteArray()
            ex.sendResponseHeaders(status, bytes.size.toLong())
            ex.responseBody.use { it.write(bytes) }
        }
    }

    private val checkoutJson = """{"id":"co_AAAAAAAAAAAAAAAAAAAAAA",
        "checkout_url":"http://192.168.1.20:5173/checkout/co_AAAAAAAAAAAAAAAAAAAAAA","status":"pending",
        "amount":"12.5","amount_base_units":"12500000","token":{"symbol":"USDC","address":"0x1c7D","decimals":6},
        "merchant":"0x3CC8","chain_id":11155111,"merchant_reference":"","created_at":"2026-11-09T10:00:00Z",
        "expires_at":"2026-11-09T10:15:00Z","tx_hash":null,"typed_data":{}}"""

    @Test fun create_sendsAmountAndKey() {
        respond("/api/checkouts", 201, checkoutJson)
        val c = HttpCheckoutApi("$base/", "secret-key").create("12.50")

        assertEquals("POST", seen["method"])
        assertEquals("secret-key", seen["key"])
        assertEquals("application/json", seen["type"])
        assertEquals("""{"amount":"12.50"}""", seen["body"])
        assertEquals("co_AAAAAAAAAAAAAAAAAAAAAA", c.id)
        assertEquals(CheckoutStatus.PENDING, c.status)
        assertEquals("12.5", c.amount)
        assertEquals("USDC", c.symbol)
        assertEquals(java.time.Instant.parse("2026-11-09T10:15:00Z").toEpochMilli(), c.expiresAtMillis)
        assertNull(c.txHash)
    }

    @Test fun get_readsStatusAndTxHash() {
        respond("/api/checkouts/co_AAAAAAAAAAAAAAAAAAAAAA", 200,
            checkoutJson.replace("\"pending\"", "\"confirmed\"").replace("\"tx_hash\":null", "\"tx_hash\":\"0xabc\""))
        val c = HttpCheckoutApi(base, "k").get("co_AAAAAAAAAAAAAAAAAAAAAA")
        assertEquals("GET", seen["method"])
        assertNull("reads don't send the key", seen["key"])
        assertEquals(CheckoutStatus.CONFIRMED, c.status)
        assertEquals("0xabc", c.txHash)
    }

    @Test fun errorBody_becomesApiException() {
        respond("/api/checkouts", 401, """{"error":"missing or invalid API key","code":"unauthorized"}""")
        val e = expectApiError { HttpCheckoutApi(base, "wrong").create("1") }
        assertEquals(401, e.status)
        assertEquals("unauthorized", e.code)
        assertEquals("missing or invalid API key", e.message)
    }

    @Test fun nonJsonError_stillAnApiException() {
        respond("/api/checkouts", 502, "bad gateway")
        val e = expectApiError { HttpCheckoutApi(base, "k").create("1") }
        assertEquals(502, e.status)
        assertEquals("http_error", e.code)
    }

    @Test fun unknownStatus_isBadResponse() {
        respond("/api/checkouts", 201, checkoutJson.replace("\"pending\"", "\"weird\""))
        assertEquals("bad_response", expectApiError { HttpCheckoutApi(base, "k").create("1") }.code)
    }

    @Test fun unreachable_isStatusZero() {
        server.stop(0)
        val e = expectApiError { HttpCheckoutApi(base, "k").get("co_x") }
        assertEquals(0, e.status)
        assertEquals("unreachable", e.code)
    }

    private fun expectApiError(block: () -> Unit): ApiException {
        try {
            block()
        } catch (e: ApiException) {
            return e
        }
        fail("expected ApiException")
        throw AssertionError()
    }
}
