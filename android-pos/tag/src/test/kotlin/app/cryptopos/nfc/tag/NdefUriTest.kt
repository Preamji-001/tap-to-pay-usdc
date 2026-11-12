package app.cryptopos.nfc.tag

import org.junit.jupiter.api.Assertions.assertArrayEquals
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertThrows

class NdefUriTest {
    @Test fun encode_httpsUrl_usesPrefix04() =
        assertArrayEquals(hex("D1 01 0D 55 04") + "example.com/".toByteArray(), NdefUri.encode("https://example.com/"))

    @Test fun encode_nonHttpsUrl_usesPrefix00() =
        assertArrayEquals(hex("D1 01 0A 55 00") + "ftp://a.b".toByteArray(), NdefUri.encode("ftp://a.b"))

    @Test fun encode_metamaskLink_payloadLengthCorrect() {
        val url = "https://metamask.app.link/dapp/example.com/checkout/co_test"
        val m = NdefUri.encode(url)
        assertEquals(url.length - 8 + 1, m[2].toInt() and 0xFF)
        assertEquals(4 + url.length - 8 + 1, m.size)
    }

    @Test fun encode_payloadOver255_throws() {
        assertThrows<IllegalArgumentException> { NdefUri.encode("https://" + "a".repeat(255)) }
    }

    @Test fun encode_payloadExactly255_ok() {
        assertEquals(259, NdefUri.encode("https://" + "a".repeat(254)).size)
    }

    @Test fun encode_nonAscii_throws() {
        assertThrows<IllegalArgumentException> { NdefUri.encode("https://exämple.com") }
    }

    @Test fun encode_empty_throws() {
        assertThrows<IllegalArgumentException> { NdefUri.encode("") }
    }
}
