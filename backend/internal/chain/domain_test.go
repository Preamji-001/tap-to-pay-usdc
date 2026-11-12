package chain

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

var sepoliaUSDC = common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")

// Value read from DOMAIN_SEPARATOR() on Sepolia on 2026-10-28.
var sepoliaUSDCDomain = common.HexToHash("0xb90e5057db141a932946e64d09ccb7ffc9b00bd79fec26f698d29af0c83320a6")

func TestDomainSeparatorSepoliaUSDC(t *testing.T) {
	got := DomainSeparator("USDC", "2", 11155111, sepoliaUSDC)
	if got != sepoliaUSDCDomain {
		t.Fatalf("DomainSeparator = %s, want %s", got, sepoliaUSDCDomain)
	}
}
