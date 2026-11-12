package app.cryptopos.nfc.tag

/**
 * Builds an NDEF message holding exactly one URI record (NFC Forum RTD-URI).
 *
 * Layout (short record):
 *   D1          flags: MB (first) | ME (last) | SR (1-byte length) | TNF 1 (well-known type)
 *   01          type length = 1
 *   <len>       payload length (1 byte, so at most 255)
 *   55          type 'U' = URI
 *   <prefix>    0x04 stands for "https://"; 0x00 means "no abbreviation"
 *   <rest>      the remaining URL bytes, ASCII
 */
object NdefUri {
    private const val HEADER: Byte = 0xD1.toByte()
    private const val TYPE_LENGTH: Byte = 0x01
    private const val TYPE_URI: Byte = 0x55
    private const val HTTPS = "https://"
    private const val PREFIX_HTTPS: Byte = 0x04
    private const val PREFIX_NONE: Byte = 0x00
    private const val MAX_SHORT_PAYLOAD = 255

    fun encode(url: String): ByteArray {
        require(url.isNotEmpty()) { "URL is empty" }
        require(url.all { it.code in 0x21..0x7E }) { "URL must be printable ASCII without spaces: $url" }

        val (prefix, rest) =
            if (url.startsWith(HTTPS)) PREFIX_HTTPS to url.removePrefix(HTTPS)
            else PREFIX_NONE to url
        val payloadLength = 1 + rest.length
        require(payloadLength <= MAX_SHORT_PAYLOAD) {
            "URL too long for a short NDEF record: payload $payloadLength > $MAX_SHORT_PAYLOAD bytes"
        }

        return byteArrayOf(HEADER, TYPE_LENGTH, payloadLength.toByte(), TYPE_URI, prefix) +
            rest.toByteArray(Charsets.US_ASCII)
    }
}
