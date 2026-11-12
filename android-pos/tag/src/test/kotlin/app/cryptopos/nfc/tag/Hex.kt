package app.cryptopos.nfc.tag

/** Test helper: "D1 01 0D" → bytes. Spaces are ignored. */
fun hex(s: String): ByteArray {
    val clean = s.replace(" ", "")
    require(clean.length % 2 == 0) { "odd hex length: $s" }
    return ByteArray(clean.length / 2) { clean.substring(it * 2, it * 2 + 2).toInt(16).toByte() }
}
