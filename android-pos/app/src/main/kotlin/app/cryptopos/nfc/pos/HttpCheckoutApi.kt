package app.cryptopos.nfc.pos

import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder
import java.time.Instant

/**
 * The checkout API over plain HttpURLConnection. Blocking: call it off the main thread.
 * Creating needs the merchant API key; reading a checkout does not.
 */
class HttpCheckoutApi(baseUrl: String, private val apiKey: String) : CheckoutApi {
    private val base = baseUrl.trimEnd('/')

    override fun create(amount: String): Checkout =
        request("POST", "/api/checkouts", JSONObject().put("amount", amount).toString())

    override fun get(id: String): Checkout =
        request("GET", "/api/checkouts/" + URLEncoder.encode(id, "UTF-8"), null)

    private fun request(method: String, path: String, body: String?): Checkout {
        val conn = try {
            URL(base + path).openConnection() as HttpURLConnection
        } catch (e: IOException) {
            throw ApiException(0, "unreachable", "cannot reach $base")
        }
        try {
            conn.requestMethod = method
            conn.connectTimeout = TIMEOUT_MS
            conn.readTimeout = TIMEOUT_MS
            conn.setRequestProperty("Accept", "application/json")
            if (body != null) {
                conn.doOutput = true
                conn.setRequestProperty("Content-Type", "application/json")
                conn.setRequestProperty("X-API-Key", apiKey)
                conn.outputStream.use { it.write(body.toByteArray()) }
            }
            val status = conn.responseCode
            val stream = if (status in 200..299) conn.inputStream else conn.errorStream
            val text = stream?.use { it.readBytes().decodeToString() }.orEmpty()
            if (status !in 200..299) {
                val err = runCatching { JSONObject(text) }.getOrNull()
                throw ApiException(
                    status,
                    err?.optString("code").orEmpty().ifEmpty { "http_error" },
                    err?.optString("error").orEmpty().ifEmpty { "HTTP $status" },
                )
            }
            return parse(text)
        } catch (e: IOException) {
            throw ApiException(0, "unreachable", "cannot reach $base")
        } finally {
            conn.disconnect()
        }
    }

    companion object {
        private const val TIMEOUT_MS = 10_000

        fun parse(text: String): Checkout = try {
            val o = JSONObject(text)
            Checkout(
                id = o.getString("id"),
                checkoutUrl = o.getString("checkout_url"),
                status = CheckoutStatus.parse(o.getString("status")),
                amount = o.getString("amount"),
                symbol = o.getJSONObject("token").getString("symbol"),
                expiresAtMillis = Instant.parse(o.getString("expires_at")).toEpochMilli(),
                txHash = if (o.isNull("tx_hash")) null else o.getString("tx_hash"),
            )
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            throw ApiException(0, "bad_response", "unexpected response from the server")
        }
    }
}
