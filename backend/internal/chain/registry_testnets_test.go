//go:build testnets

package chain

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

// Run with, for example:
//
//	SEPOLIA_RPC_URL=… BASE_SEPOLIA_RPC_URL=https://sepolia.base.org \
//	AMOY_RPC_URL=https://polygon-amoy-bor-rpc.publicnode.com \
//	go test -tags testnets -run Live ./internal/chain/
//
// A network whose RPC variable is unset is skipped.
func TestCheckNetworkLive(t *testing.T) {
	for _, key := range KnownNetworkKeys() {
		t.Run(key, func(t *testing.T) {
			n, _ := KnownNetwork(key)
			n.RPCURL = os.Getenv(strings.ToUpper(strings.ReplaceAll(key, "-", "_")) + "_RPC_URL")
			if n.RPCURL == "" {
				t.Skip("RPC URL not set")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := ethclient.DialContext(ctx, n.RPCURL)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer c.Close()

			if err := CheckNetwork(ctx, c, n); err != nil {
				t.Fatalf("CheckNetwork: %v", err)
			}

			// Another network's USDC address must be rejected on this chain.
			wrong := n
			for _, other := range knownNetworks {
				if other.USDC != n.USDC {
					wrong.USDC = other.USDC
					break
				}
			}
			if err := CheckNetwork(ctx, c, wrong); err == nil {
				t.Fatalf("CheckNetwork accepted %s on %s", wrong.USDC.Hex(), key)
			} else {
				t.Logf("wrong address rejected as expected: %v", err)
			}
		})
	}
}
