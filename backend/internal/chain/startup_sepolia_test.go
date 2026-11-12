//go:build sepolia

package chain

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

// Run with: SEPOLIA_RPC_URL=... go test -tags sepolia ./internal/chain/
func TestCheckTokenLiveSepolia(t *testing.T) {
	url := os.Getenv("SEPOLIA_RPC_URL")
	if url == "" {
		t.Skip("SEPOLIA_RPC_URL not set; skipping live Sepolia check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := ethclient.DialContext(ctx, url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if err := CheckToken(ctx, client, sepoliaParams); err != nil {
		t.Fatalf("CheckToken against live Sepolia: %v", err)
	}
}
