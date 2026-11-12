package app.cryptopos.nfc

import android.util.Log
import java.util.concurrent.CopyOnWriteArrayList

/** In-memory log of the APDU exchange (last 200 lines), mirrored to Logcat under tag "PosHce". */
object ApduLog {
    private const val MAX_LINES = 200
    private const val TAG = "PosHce"

    private val buffer = ArrayDeque<String>()
    private val listeners = CopyOnWriteArrayList<() -> Unit>()

    fun add(line: String) {
        Log.d(TAG, line)
        synchronized(buffer) {
            buffer.addLast(line)
            while (buffer.size > MAX_LINES) buffer.removeFirst()
        }
        listeners.forEach { it() }
    }

    fun lines(): List<String> = synchronized(buffer) { buffer.toList() }

    fun addListener(l: () -> Unit) { listeners.add(l) }

    fun removeListener(l: () -> Unit) { listeners.remove(l) }
}

fun ByteArray.toHex(): String = joinToString("") { "%02X".format(it) }
