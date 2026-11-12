# crypto-pos NFC: POS phone emulates an NDEF tag

The POS phone (OnePlus 6T) takes an amount, creates a checkout on the backend
([the backend](../backend)), and then pretends to be an NFC tag holding
that checkout's link. A customer's iPhone reads the tag without any app installed, opens the page in MetaMask
and signs. The POS shows "Paid" only once the backend reports `confirmed`. Milestones N1 (the tag) and N5 (the POS
screens) of the NFC → MetaMask → USDC plan (kept in an earlier proof-of-concept repo, not included here).

## Layout

- **`tag/`:** pure Kotlin, no Android. `NdefUri` builds the NDEF URI record, and `NdefTag` is the NFC Forum
  Type 4 tag state machine that answers each APDU. All the protocol logic lives here, and it is unit-tested on
  the JVM.
- **`app/`:** the Android app.
  - `PosHceService` receives APDUs for the AID `D2760000850101` and forwards them to `NdefTag`.
  - `PosHceService` answers with the tag in `TagHolder`, or "not found" when no sale is waiting for a tap.
  - `pos/`:
    - `PosController` runs one sale: create → serve while `pending` → poll every 2 s → final. It's tested with
      a fake API and a manual scheduler.
    - `HttpCheckoutApi` is the backend client.
    - `screenFor` maps each state to what the screen shows.
  - `MainActivity` is the POS screen: amount, status, countdown, "New sale", and the APDU log as a debug panel.
    The sale in progress survives an app restart.

## POS settings

In `local.properties`, which git ignores, so the API key is never committed:

```properties
pos.apiBaseUrl=http://192.168.1.20:8080   # the backend, reachable from the phone (LAN IP or a tunnel)
pos.apiKey=<MERCHANT_API_KEY from the backend's .env>
pos.tagMode=metamask                      # metamask: serve the MetaMask dapp link; plain: the checkout URL itself
```

The values are compiled into `BuildConfig`, so rebuild after changing them. Debug builds allow plain HTTP for
LAN testing. **Limitation:** the POS holds the merchant API key. That's acceptable for this POC; production
would use per-terminal credentials.

## Pipeline (the gate)

```bash
./scripts/pipeline.sh
```

This is one command: `./gradlew check :app:assembleDebug`. GitHub Actions runs the same script on every PR and every
push to `main` (`.github/workflows/pipeline.yml`). **Green means every simulated tap passed.**

What it runs:

- **Protocol tests** (`tag/src/test`): exact bytes for every command and error case.
- **Tap simulations, two levels.** `tag/src/testFixtures` holds a `FakeTagReader` that reads the tag the way a phone
  does: it takes the read size and file ids from the tag's own answers, then decodes the URL. It also holds the
  `TapScenarios` it runs:
  - normal tap;
  - 20 taps in a row;
  - phone pulled away after every possible step, with and without a deactivation event;
  - other AIDs probed first;
  - an Android-style reader;
  - 2000 garbled commands;
  - MetaMask link, longest possible URL, and plain `http`.

  The scenarios run against `NdefTag` (`TapSimulationTest`) and against the **real `PosHceService` on Android 11
  via Robolectric** (`app/src/test/.../PosHceServiceSimulationTest`). In the second case each APDU goes through
  `processCommandApdu`, and each departure through `onDeactivated`, exactly as Android delivers them.
- **Lint** (errors fail the build) and the debug APK build.

**What the simulations can't cover:** the radio, whether iOS notices an Android-emulated tag, Android's AID
routing on the device, and the iOS banner or MetaMask hand-off. Those are in the on-device test below.

## Build

```bash
export JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home"
./gradlew :tag:test                       # protocol tests, no phone needed
./gradlew :app:assembleDebug :app:lintDebug
./gradlew :app:installDebug               # with the 6T plugged in (USB debugging on)
adb logcat -s PosHce                      # the same APDU log, in the terminal
```

## What the reader sends (and what we answer)

| # | Reader → tag | Tag → reader |
|---|---|---|
| 1 | `00A4040007D2760000850101[00]` SELECT NDEF app | `9000` |
| 2 | `00A4000C02E103` SELECT capability container | `9000` |
| 3 | `00B000000F` READ 15 bytes | `000F 20 003B 0034 0406 E104 0200 00 FF` + `9000` |
| 4 | `00A4000C02E104` SELECT NDEF file | `9000` |
| 5 | `00B0000002` READ 2 bytes | message length + `9000` |
| 6 | `00B0 <offset> <len>` READ chunks | NDEF bytes + `9000` |

Writes are refused (`6982`). The tag is read-only.

## On-device test (OnePlus 6T = POS, iPhone 15 = customer)

1. Run `adb shell pm list features | grep nfc`. It must list `android.hardware.nfc.hce`.
2. Set the POS settings, install, open the app, and turn NFC on. Charge any amount so the tag is served. If taps stop arriving, exempt the app from battery optimization.
3. Unlock the iPhone with the screen on. Hold its top edge to the middle of the 6T's back for about a second.
   Watch the APDU log, and note the banner on the iPhone.
4. Do 20 taps, pressing **Add marker to log** between them. Record successes, partial reads (`✕` before the last
   READ), and behaviour with the 6T's screen off, and when it is locked with the screen on.
5. Charge an amount with `pos.tagMode=metamask` and tap. Record whether MetaMask opens and every screen in
   between. This is N3.

## N1 device results

_Not run yet._
