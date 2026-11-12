package app.cryptopos.nfc.tag.sim

import app.cryptopos.nfc.tag.sim.FakeTagReader.Companion.hex
import app.cryptopos.nfc.tag.sim.FakeTagReader.Companion.toHex
import kotlin.random.Random

/**
 * Real-world tap situations, as sequences of reader events. Test-framework independent (they throw
 * AssertionError), so the JUnit 5 tests in :tag and the JUnit 4 / Robolectric tests in :app share them.
 */
object TapScenarios {
    /** One ordinary tap. */
    fun normalTap(tag: TagEndpoint, url: String) {
        expectUrl(url, FakeTagReader(tag).tap(), "normal tap")
    }

    /** The N1 acceptance check: many taps in a row, same URL every time. */
    fun repeatedTaps(tag: TagEndpoint, url: String, times: Int) {
        val reader = FakeTagReader(tag)
        repeat(times) { expectUrl(url, reader.tap(), "tap ${it + 1} of $times") }
    }

    /**
     * The phone is pulled away after 1, 2, 3, ... commands, with and without the tag being told
     * (a glitch can end a tap with no deactivation event). The next tap must always read the full URL.
     */
    fun droppedAtEveryStep(tag: TagEndpoint, url: String) {
        val reader = FakeTagReader(tag)
        expectUrl(url, reader.tap(), "baseline tap")
        val fullTap = reader.lastTapCommands
        for (goodbye in listOf(true, false)) {
            for (n in 1 until fullTap) {
                val partial = reader.tap(dropAfter = n, goodbye = goodbye)
                if (partial != null) throw AssertionError("tap dropped after $n commands still returned a URL")
                expectUrl(url, reader.tap(), "tap after a drop at command $n (goodbye=$goodbye)")
            }
        }
    }

    /** Some readers probe other applications first, e.g. the old NDEF v1 AID. */
    fun otherAidProbedFirst(tag: TagEndpoint, url: String) {
        for (aid in listOf("D2760000850100", "A0000000031010")) {
            val reply = tag.transceive(hex("00A4040007") + hex(aid) + hex("00"))
            if (!reply.contentEquals(hex("6A82"))) throw AssertionError("SELECT $aid -> ${reply.toHex()}, want 6A82")
        }
        expectUrl(url, FakeTagReader(tag).tap(), "tap after other AIDs were probed")
    }

    /** An Android phone reads tags slightly differently (no Le on SELECT AID, P2 = 00). */
    fun androidStyleReader(tag: TagEndpoint, url: String) {
        expectUrl(url, FakeTagReader(tag, FakeTagReader.Style.ANDROID).tap(), "Android-style reader")
    }

    /** Corrupted or random commands must get a status reply and never break the tag for the next tap. */
    fun garbledCommands(tag: TagEndpoint, url: String, count: Int = 2000, seed: Int = 42) {
        val random = Random(seed)
        repeat(count) { i ->
            val apdu = random.nextBytes(random.nextInt(0, 24))
            if (random.nextBoolean() && apdu.isNotEmpty()) apdu[0] = 0x00 // reach the INS handling too
            val reply = try {
                tag.transceive(apdu)
            } catch (e: Throwable) {
                throw AssertionError("garbled command #$i ${apdu.toHex()} threw", e)
            }
            if (reply.size < 2) throw AssertionError("garbled command #$i ${apdu.toHex()} got no status word")
            if (i % 100 == 99) tag.fieldLost()
        }
        tag.fieldLost()
        expectUrl(url, FakeTagReader(tag).tap(), "tap after $count garbled commands")
    }

    private fun expectUrl(want: String, got: String?, what: String) {
        if (want != got) throw AssertionError("$what: read '$got', want '$want'")
    }
}
