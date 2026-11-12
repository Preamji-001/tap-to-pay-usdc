package app.cryptopos.nfc

import app.cryptopos.nfc.pos.TagServer
import app.cryptopos.nfc.tag.NdefTag

/** The tag the HCE service answers with, shared between the screen and the service. Null: not serving. */
object TagHolder : TagServer {
    @Volatile
    var tag: NdefTag? = null
        private set

    @Volatile
    var url: String? = null
        private set

    override fun serve(url: String) {
        tag = NdefTag(url)
        this.url = url
        ApduLog.add("serving $url")
    }

    override fun stop() {
        tag = null
        url = null
        ApduLog.add("stopped serving")
    }
}
