package app.cryptopos.nfc.tag.sim

/** A tag answered with something a real reader would reject. */
class TagReadException(message: String) : RuntimeException(message)

/**
 * Reads an NDEF URL from a Type 4 tag the way a phone does. Every step is derived from the tag's own
 * answers: the read size (MLe), the NDEF file id and the max size come from the capability container,
 * not from constants. So a wrong CC byte breaks the read here, as it would on the iPhone.
 *
 * [Style.IOS] sends SELECT AID with a trailing Le and P2=0C. [Style.ANDROID] sends no Le and P2=00.
 */
class FakeTagReader(private val tag: TagEndpoint, private val style: Style = Style.IOS) {
    enum class Style { IOS, ANDROID }

    private class Dropped : RuntimeException()

    /** Commands sent in the most recent tap. */
    var lastTapCommands = 0
        private set

    /**
     * One tap. Stops after [dropAfter] commands (the phone is pulled away) and returns null in that case.
     * [goodbye] = whether the field loss reaches the tag; a glitch can end a tap with no deactivation event.
     */
    fun tap(dropAfter: Int = Int.MAX_VALUE, goodbye: Boolean = true): String? {
        lastTapCommands = 0
        return try {
            read(dropAfter)
        } catch (d: Dropped) {
            null
        } finally {
            if (goodbye) tag.fieldLost()
        }
    }

    private fun read(dropAfter: Int): String {
        val p2 = if (style == Style.IOS) 0x0C else 0x00
        val selectAid = hex("00A4040007D2760000850101") + if (style == Style.IOS) hex("00") else ByteArray(0)
        send(selectAid, dropAfter)
        send(hex("00A400") + byteArrayOf(p2.toByte(), 0x02) + hex("E103"), dropAfter)

        val cc = send(readBinary(0, 15), dropAfter)
        check(cc.size == 15) { "CC read returned ${cc.size} bytes" }
        check(u16(cc, 0) == 0x000F) { "CCLEN ${u16(cc, 0)}" }
        check(cc[2].toInt() and 0xF0 == 0x20) { "mapping version ${cc[2]}" }
        val mle = u16(cc, 3)
        check(mle >= 0x0F) { "MLe $mle below the spec minimum" }
        check(cc[7] == 0x04.toByte() && cc[8] == 0x06.toByte()) { "no NDEF File Control TLV" }
        val ndefFileId = cc.copyOfRange(9, 11)
        val maxSize = u16(cc, 11)
        check(cc[13] == 0x00.toByte()) { "NDEF file not readable" }

        send(hex("00A400") + byteArrayOf(p2.toByte(), 0x02) + ndefFileId, dropAfter)
        val nlen = u16(send(readBinary(0, 2), dropAfter), 0)
        check(nlen in 1..maxSize - 2) { "NLEN $nlen outside 1..${maxSize - 2}" }

        val message = java.io.ByteArrayOutputStream()
        var offset = 2
        while (offset < 2 + nlen) {
            val want = minOf(mle, 2 + nlen - offset)
            val chunk = send(readBinary(offset, want), dropAfter)
            check(chunk.size == want) { "asked $want bytes at $offset, got ${chunk.size}" }
            message.write(chunk)
            offset += chunk.size
        }
        return decodeUri(message.toByteArray())
    }

    private fun send(apdu: ByteArray, dropAfter: Int): ByteArray {
        if (lastTapCommands >= dropAfter) throw Dropped()
        lastTapCommands++
        val reply = tag.transceive(apdu)
        if (reply.size < 2) throw TagReadException("${apdu.toHex()} -> reply shorter than a status word")
        val sw = reply.copyOfRange(reply.size - 2, reply.size)
        if (!sw.contentEquals(hex("9000"))) throw TagReadException("${apdu.toHex()} -> SW ${sw.toHex()}")
        return reply.copyOfRange(0, reply.size - 2)
    }

    companion object {
        private val URI_PREFIXES = mapOf(0 to "", 1 to "http://www.", 2 to "https://www.", 3 to "http://", 4 to "https://")

        /** Decodes a one-record NDEF message holding a short URI record (NFC Forum RTD-URI). */
        fun decodeUri(msg: ByteArray): String {
            check(msg.size >= 5) { "message too short" }
            val header = msg[0].toInt() and 0xFF
            check(header and 0x80 != 0 && header and 0x40 != 0) { "not a single record (MB/ME)" }
            check(header and 0x10 != 0) { "not a short record" }
            check(header and 0x08 == 0) { "unexpected ID field" }
            check(header and 0x07 == 0x01) { "TNF is not well-known" }
            check(msg[1] == 0x01.toByte() && msg[3] == 'U'.code.toByte()) { "record type is not U" }
            val payloadLength = msg[2].toInt() and 0xFF
            check(msg.size == 4 + payloadLength) { "payload length $payloadLength vs message ${msg.size}" }
            val prefix = URI_PREFIXES[msg[4].toInt() and 0xFF] ?: error("unknown URI prefix ${msg[4]}")
            return prefix + String(msg, 5, payloadLength - 1, Charsets.US_ASCII)
        }

        fun readBinary(offset: Int, le: Int) =
            hex("00B0") + byteArrayOf((offset shr 8).toByte(), offset.toByte(), le.toByte())

        fun hex(s: String): ByteArray {
            val clean = s.replace(" ", "")
            return ByteArray(clean.length / 2) { clean.substring(it * 2, it * 2 + 2).toInt(16).toByte() }
        }

        fun ByteArray.toHex(): String = joinToString("") { "%02X".format(it) }

        private fun u16(b: ByteArray, at: Int) = ((b[at].toInt() and 0xFF) shl 8) or (b[at + 1].toInt() and 0xFF)
    }
}
