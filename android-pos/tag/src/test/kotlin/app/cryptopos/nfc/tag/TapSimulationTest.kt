package app.cryptopos.nfc.tag

import app.cryptopos.nfc.tag.sim.TapScenarios
import app.cryptopos.nfc.tag.sim.asEndpoint
import org.junit.jupiter.api.Test

/**
 * Simulated taps against the protocol logic alone. A fake reader drives NdefTag the way a phone does,
 * deriving every step from the tag's own answers, and decodes the URL back at the end.
 */
class TapSimulationTest {
    private val url = "https://example.com/checkout/co_test"
    private fun endpoint(u: String = url) = NdefTag(u).asEndpoint()

    @Test fun normalTap_readsBackExactUrl() = TapScenarios.normalTap(endpoint(), url)
    @Test fun twentyTaps_allReadSameUrl() = TapScenarios.repeatedTaps(endpoint(), url, times = 20)
    @Test fun droppedAtEveryStep_nextTapStillWorks() = TapScenarios.droppedAtEveryStep(endpoint(), url)
    @Test fun otherAidProbedFirst_thenTapWorks() = TapScenarios.otherAidProbedFirst(endpoint(), url)
    @Test fun androidStyleReader_readsUrl() = TapScenarios.androidStyleReader(endpoint(), url)
    @Test fun garbledCommands_neverBreakTag() = TapScenarios.garbledCommands(endpoint(), url)

    @Test fun metamaskLink_roundTrips() {
        val link = "https://metamask.app.link/dapp/pos.example.com/checkout/co_AbCdEfGhIjKlMnOpQrStUv"
        TapScenarios.normalTap(endpoint(link), link)
    }

    @Test fun longestUrl_roundTrips() {
        // payload 255 bytes -> NDEF message 259 bytes -> file 261 bytes: NLEN high byte is 1 and reads cross offset 256
        val longest = "https://" + "a".repeat(254)
        TapScenarios.normalTap(endpoint(longest), longest)
        TapScenarios.droppedAtEveryStep(endpoint(longest), longest)
    }

    @Test fun nonHttpsUrl_roundTrips() {
        val plain = "http://192.168.1.20:5173/checkout/co_test"
        TapScenarios.normalTap(endpoint(plain), plain)
    }
}
