package app.cryptopos.nfc

import android.nfc.cardemulation.HostApduService
import android.os.Bundle
import app.cryptopos.nfc.tag.NdefTag

/**
 * Android hands us every APDU sent to our AID (D2760000850101, see res/xml/apduservice.xml).
 * All protocol logic lives in [NdefTag]; this class forwards to the tag currently served
 * ([TagHolder]) and logs. With no sale in progress it answers "not found", so a tap shows nothing.
 */
class PosHceService : HostApduService() {
    override fun processCommandApdu(commandApdu: ByteArray, extras: Bundle?): ByteArray {
        ApduLog.add("→ ${commandApdu.toHex()}")
        val response = TagHolder.tag?.process(commandApdu) ?: NdefTag.SW_NOT_FOUND.copyOf()
        ApduLog.add("← ${response.toHex()}")
        return response
    }

    override fun onDeactivated(reason: Int) {
        val why = when (reason) {
            DEACTIVATION_LINK_LOSS -> "link lost"
            DEACTIVATION_DESELECTED -> "deselected"
            else -> reason.toString()
        }
        ApduLog.add("✕ deactivated $why")
        TagHolder.tag?.reset()
    }
}
