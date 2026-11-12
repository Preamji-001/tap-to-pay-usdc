package app.cryptopos.nfc.tag.sim

import app.cryptopos.nfc.tag.NdefTag

/**
 * Everything a reader can do to a tag, as seen by our code. The NFC chip and Android turn the radio
 * signal into exactly these events: an APDU arrives, or the reader leaves the field.
 */
interface TagEndpoint {
    fun transceive(apdu: ByteArray): ByteArray
    fun fieldLost()
}

fun NdefTag.asEndpoint(): TagEndpoint = object : TagEndpoint {
    override fun transceive(apdu: ByteArray) = process(apdu)
    override fun fieldLost() = reset()
}
