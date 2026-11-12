package app.cryptopos.nfc.tag

import java.io.ByteArrayOutputStream
import org.junit.jupiter.api.Assertions.assertArrayEquals
import org.junit.jupiter.api.Test

class NdefTagTest {
    private val url = "https://example.com/checkout/co_test"
    private val msg = NdefUri.encode(url)

    private val selApp = hex("00A4040007D2760000850101") // SELECT NDEF app, no Le
    private val selAppLe = selApp + hex("00")             // same, with trailing Le
    private val selCC = hex("00A4000C02E103")
    private val selNdef = hex("00A4000C02E104")
    private fun read(off: Int, le: Int) =
        hex("00B0") + byteArrayOf((off shr 8).toByte(), off.toByte(), le.toByte())

    private val ok = hex("9000")

    /** The exact command sequence an iPhone sends to a Type 4 tag; asserts every reply. */
    private fun runIphoneScript(t: NdefTag) {
        assertArrayEquals(ok, t.process(selAppLe))
        assertArrayEquals(ok, t.process(selCC))
        assertArrayEquals(hex("000F 20 003B 0034 0406 E104 0200 00 FF 9000"), t.process(read(0, 15)))
        assertArrayEquals(ok, t.process(selNdef))
        assertArrayEquals(byteArrayOf(0, msg.size.toByte()) + ok, t.process(read(0, 2)))
        // read the message in 59-byte (MLe) chunks, as iOS does
        val out = ByteArrayOutputStream()
        var off = 2
        while (off < 2 + msg.size) {
            val r = t.process(read(off, 59))
            assertArrayEquals(ok, r.copyOfRange(r.size - 2, r.size))
            out.write(r, 0, r.size - 2)
            off += r.size - 2
        }
        assertArrayEquals(msg, out.toByteArray())
    }

    @Test fun iphoneScript_readsFullMessage() = runIphoneScript(NdefTag(url))

    @Test fun selectApp_withAndWithoutLe_ok() {
        val t = NdefTag(url)
        assertArrayEquals(ok, t.process(selApp))
        assertArrayEquals(ok, t.process(selAppLe))
    }

    @Test fun selectUnknownAid_notFound() =
        assertArrayEquals(hex("6A82"), NdefTag(url).process(hex("00A4040007A0000000031010")))

    @Test fun selectFile_beforeApp_notFound() =
        assertArrayEquals(hex("6A82"), NdefTag(url).process(selCC))

    @Test fun selectFile_p2Zero_ok() {
        val t = NdefTag(url)
        t.process(selApp)
        assertArrayEquals(ok, t.process(hex("00A4000002E103")))
    }

    @Test fun selectUnknownFile_notFound_andClearsSelection() {
        val t = NdefTag(url)
        t.process(selApp)
        t.process(selCC)
        assertArrayEquals(hex("6A82"), t.process(hex("00A4000C02E105")))
        assertArrayEquals(hex("6986"), t.process(read(0, 2)))
    }

    @Test fun read_noFileSelected_6986() {
        val t = NdefTag(url)
        t.process(selApp)
        assertArrayEquals(hex("6986"), t.process(read(0, 2)))
    }

    @Test fun read_leZero_returnsUpTo256() {
        val t = NdefTag(url)
        t.process(selApp)
        t.process(selNdef)
        assertArrayEquals(byteArrayOf(0, msg.size.toByte()) + msg + ok, t.process(read(0, 0)))
    }

    @Test fun read_pastEnd_returnsRemainder() {
        val t = NdefTag(url)
        t.process(selApp)
        t.process(selNdef)
        assertArrayEquals(msg.copyOfRange(msg.size - 3, msg.size) + ok, t.process(read(2 + msg.size - 3, 59)))
    }

    @Test fun read_offsetBeyondFile_6B00() {
        val t = NdefTag(url)
        t.process(selApp)
        t.process(selNdef)
        assertArrayEquals(hex("6B00"), t.process(read(2 + msg.size + 1, 1)))
    }

    @Test fun updateBinary_refused() {
        val t = NdefTag(url)
        t.process(selApp)
        t.process(selNdef)
        assertArrayEquals(hex("6982"), t.process(hex("00D6000001FF")))
    }

    @Test fun unknownIns_6D00() =
        assertArrayEquals(hex("6D00"), NdefTag(url).process(hex("00CA000000")))

    @Test fun wrongCla_6E00() =
        assertArrayEquals(hex("6E00"), NdefTag(url).process(hex("80A4040007D2760000850101")))

    @Test fun selectApp_midSession_clearsFileSelection() {
        val t = NdefTag(url)
        t.process(selApp)
        t.process(selNdef)
        t.process(selApp)
        assertArrayEquals(hex("6986"), t.process(read(0, 2)))
    }

    @Test fun reset_afterPartialRead_fullScriptWorksAgain() {
        val t = NdefTag(url)
        t.process(selApp)
        t.process(selNdef)
        t.process(read(0, 2))
        t.reset()
        assertArrayEquals(hex("6986"), t.process(read(0, 2)))
        runIphoneScript(t)
    }

    @Test fun garbageApdus_returnErrorNeverThrow() {
        val t = NdefTag(url)
        for (apdu in listOf(byteArrayOf(), hex("00"), hex("00A4"), hex("00A404"), hex("00A4040007D276"), hex("00B0000002FF"))) {
            assertArrayEquals(hex("6700"), t.process(apdu), "apdu=${apdu.joinToString("") { "%02X".format(it) }}")
        }
    }
}
