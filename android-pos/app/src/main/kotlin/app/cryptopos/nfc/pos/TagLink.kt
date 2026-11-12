package app.cryptopos.nfc.pos

import java.net.URI

enum class TagMode { METAMASK, PLAIN }

/**
 * The URL the NFC tag serves for a checkout. In METAMASK mode it is MetaMask's dapp deep link
 * (https://metamask.app.link/dapp/<host><path>), which opens the checkout page inside MetaMask's
 * browser; PLAIN serves the checkout page itself.
 */
fun tagUrl(checkoutUrl: String, mode: TagMode): String = when (mode) {
    TagMode.PLAIN -> checkoutUrl
    TagMode.METAMASK -> URI(checkoutUrl).let { "https://metamask.app.link/dapp/${it.rawAuthority}${it.rawPath}" }
}
