package app.cryptopos.nfc.tag

/**
 * A read-only NFC Forum Type 4 tag (mapping version 2.0) that serves one URL.
 *
 * The reader (an iPhone, in our case) drives the conversation:
 *   1. SELECT the NDEF application by its AID          -> 9000
 *   2. SELECT the capability container file E103       -> 9000
 *   3. READ BINARY 15 bytes: the capability container   -> CC + 9000
 *   4. SELECT the NDEF file E104                        -> 9000
 *   5. READ BINARY 2 bytes: the NDEF message length     -> NLEN + 9000
 *   6. READ BINARY the message, in chunks of up to MLe   -> bytes + 9000
 *
 * Every reply is `data + SW1 SW2`. [process] never throws: an exception escaping
 * Android's processCommandApdu would kill the HCE service.
 */
class NdefTag(url: String) {
    private val ccFile: ByteArray = byteArrayOf(
        0x00, 0x0F,             // CCLEN: this file is 15 bytes
        0x20,                   // mapping version 2.0
        0x00, 0x3B,             // MLe: at most 59 bytes per READ BINARY reply
        0x00, 0x34,             // MLc: at most 52 bytes per command
        0x04, 0x06,             // NDEF File Control TLV: tag 04, length 6
        0xE1.toByte(), 0x04,    //   NDEF file id E104
        0x02, 0x00,             //   max NDEF file size 512
        0x00,                   //   read access: granted
        0xFF.toByte(),          //   write access: denied (read-only tag)
    )

    private val ndefFile: ByteArray = NdefUri.encode(url).let { msg ->
        byteArrayOf((msg.size shr 8).toByte(), msg.size.toByte()) + msg
    }

    private var appSelected = false
    private var selectedFile: ByteArray? = null

    /** Forget all selection state. Called when the reader leaves the field. */
    fun reset() {
        appSelected = false
        selectedFile = null
    }

    fun process(apdu: ByteArray): ByteArray =
        try {
            handle(apdu).copyOf()
        } catch (e: Exception) {
            SW_UNKNOWN_ERROR.copyOf() // last-resort guard; the tests never reach this
        }

    private fun handle(apdu: ByteArray): ByteArray {
        if (apdu.size < 4) return SW_WRONG_LENGTH
        if (apdu[0] != CLA) return SW_CLA_NOT_SUPPORTED
        return when (apdu[1]) {
            INS_SELECT -> select(apdu)
            INS_READ_BINARY -> readBinary(apdu)
            INS_UPDATE_BINARY -> SW_WRITE_REFUSED
            else -> SW_INS_NOT_SUPPORTED
        }
    }

    private fun select(apdu: ByteArray): ByteArray {
        if (apdu.size < 5) return SW_WRONG_LENGTH
        val lc = apdu[4].toInt() and 0xFF
        // data may be followed by one optional Le byte
        if (apdu.size != 5 + lc && apdu.size != 5 + lc + 1) return SW_WRONG_LENGTH
        val data = apdu.copyOfRange(5, 5 + lc)
        val p1 = apdu[2].toInt() and 0xFF
        val p2 = apdu[3].toInt() and 0xFF

        if (p1 == 0x04) { // select by application name (AID)
            selectedFile = null
            appSelected = data.contentEquals(NDEF_AID)
            return if (appSelected) SW_OK else SW_NOT_FOUND
        }

        if (p1 == 0x00 && (p2 == 0x0C || p2 == 0x00) && appSelected) { // select by file id
            selectedFile = when {
                data.contentEquals(CC_FILE_ID) -> ccFile
                data.contentEquals(NDEF_FILE_ID) -> ndefFile
                else -> null
            }
            return if (selectedFile != null) SW_OK else SW_NOT_FOUND
        }

        selectedFile = null
        return SW_NOT_FOUND
    }

    private fun readBinary(apdu: ByteArray): ByteArray {
        if (apdu.size != 5) return SW_WRONG_LENGTH
        val file = selectedFile ?: return SW_NO_FILE_SELECTED
        val offset = ((apdu[2].toInt() and 0xFF) shl 8) or (apdu[3].toInt() and 0xFF)
        if (offset > file.size) return SW_WRONG_OFFSET
        val le = (apdu[4].toInt() and 0xFF).let { if (it == 0) 256 else it }
        val end = minOf(offset + le, file.size)
        return file.copyOfRange(offset, end) + SW_OK
    }

    companion object {
        private const val CLA: Byte = 0x00
        private const val INS_SELECT: Byte = 0xA4.toByte()
        private const val INS_READ_BINARY: Byte = 0xB0.toByte()
        private const val INS_UPDATE_BINARY: Byte = 0xD6.toByte()

        private val NDEF_AID = byteArrayOf(0xD2.toByte(), 0x76, 0x00, 0x00, 0x85.toByte(), 0x01, 0x01)
        private val CC_FILE_ID = byteArrayOf(0xE1.toByte(), 0x03)
        private val NDEF_FILE_ID = byteArrayOf(0xE1.toByte(), 0x04)

        val SW_OK = sw(0x90, 0x00)
        val SW_WRONG_LENGTH = sw(0x67, 0x00)
        val SW_CLA_NOT_SUPPORTED = sw(0x6E, 0x00)
        val SW_INS_NOT_SUPPORTED = sw(0x6D, 0x00)
        val SW_NOT_FOUND = sw(0x6A, 0x82)
        val SW_NO_FILE_SELECTED = sw(0x69, 0x86)
        val SW_WRONG_OFFSET = sw(0x6B, 0x00)
        val SW_WRITE_REFUSED = sw(0x69, 0x82)
        val SW_UNKNOWN_ERROR = sw(0x6F, 0x00)

        private fun sw(sw1: Int, sw2: Int) = byteArrayOf(sw1.toByte(), sw2.toByte())
    }
}
