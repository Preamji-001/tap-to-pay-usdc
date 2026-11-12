package app.cryptopos.nfc

import android.nfc.cardemulation.HostApduService
import app.cryptopos.nfc.tag.sim.FakeTagReader
import app.cryptopos.nfc.tag.sim.TagEndpoint
import app.cryptopos.nfc.tag.sim.TapScenarios
import app.cryptopos.nfc.tag.NdefTag
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * The same simulated taps, sent to the real PosHceService the way Android delivers them:
 * each APDU through processCommandApdu, and the reader leaving through onDeactivated.
 * Runs on Android 11 (API 30), the OnePlus 6T's version.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [30])
class PosHceServiceSimulationTest {
    private companion object {
        const val URL = "https://metamask.app.link/dapp/pay.example.com/checkout/co_AAAAAAAAAAAAAAAAAAAAAA"
    }

    @Before fun serve() = TagHolder.serve(URL)

    @After fun stop() = TagHolder.stop()

    private val service: PosHceService = Robolectric.setupService(PosHceService::class.java)

    private val endpoint = object : TagEndpoint {
        override fun transceive(apdu: ByteArray): ByteArray = service.processCommandApdu(apdu, null)
        override fun fieldLost() = service.onDeactivated(HostApduService.DEACTIVATION_LINK_LOSS)
    }

    @Test fun normalTap_readsBackConfiguredUrl() = TapScenarios.normalTap(endpoint, URL)
    @Test fun twentyTaps_allReadSameUrl() = TapScenarios.repeatedTaps(endpoint, URL, times = 20)
    @Test fun droppedAtEveryStep_nextTapStillWorks() = TapScenarios.droppedAtEveryStep(endpoint, URL)
    @Test fun otherAidProbedFirst_thenTapWorks() = TapScenarios.otherAidProbedFirst(endpoint, URL)
    @Test fun androidStyleReader_readsUrl() = TapScenarios.androidStyleReader(endpoint, URL)
    @Test fun garbledCommands_neverCrashService() = TapScenarios.garbledCommands(endpoint, URL)

    @Test fun deselectedMidRead_nextTapStillWorks() {
        FakeTagReader(endpoint).tap(dropAfter = 4, goodbye = false)
        service.onDeactivated(HostApduService.DEACTIVATION_DESELECTED)
        assertEquals(URL, FakeTagReader(endpoint).tap())
    }

    @Test fun tap_isRecordedInApduLog() {
        FakeTagReader(endpoint).tap()
        val log = ApduLog.lines()
        assertTrue("SELECT AID logged", log.any { it.startsWith("→ 00A4040007D2760000850101") })
        assertTrue("success reply logged", log.any { it == "← 9000" })
        assertTrue("field loss logged", log.any { it == "✕ deactivated link lost" })
    }

    @Test fun notServing_tapFindsNoTag() {
        TagHolder.stop()
        val selectNdefApp = byteArrayOf(0x00, 0xA4.toByte(), 0x04, 0x00, 0x07,
            0xD2.toByte(), 0x76, 0x00, 0x00, 0x85.toByte(), 0x01, 0x01, 0x00)
        assertArrayEquals(NdefTag.SW_NOT_FOUND, service.processCommandApdu(selectNdefApp, null))
        service.onDeactivated(HostApduService.DEACTIVATION_LINK_LOSS) // must not crash with nothing served
    }

    @Test fun newSale_servesNewUrl() {
        val next = "https://metamask.app.link/dapp/pay.example.com/checkout/co_BBBBBBBBBBBBBBBBBBBBBB"
        TagHolder.serve(next)
        assertEquals(next, FakeTagReader(endpoint).tap())
    }
}
