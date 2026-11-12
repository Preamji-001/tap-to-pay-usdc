package app.cryptopos.nfc.pos

import org.junit.Assert.assertEquals
import org.junit.Test

class TagLinkTest {
    @Test fun metamask_wrapsHostAndPath() {
        assertEquals(
            "https://metamask.app.link/dapp/pay.example.com/checkout/co_AAAAAAAAAAAAAAAAAAAAAA",
            tagUrl("https://pay.example.com/checkout/co_AAAAAAAAAAAAAAAAAAAAAA", TagMode.METAMASK),
        )
    }

    @Test fun metamask_keepsPort() {
        assertEquals(
            "https://metamask.app.link/dapp/192.168.1.20:5173/checkout/co_x",
            tagUrl("http://192.168.1.20:5173/checkout/co_x", TagMode.METAMASK),
        )
    }

    @Test fun plain_isUnchanged() {
        assertEquals("http://localhost:5173/checkout/co_x", tagUrl("http://localhost:5173/checkout/co_x", TagMode.PLAIN))
    }
}
