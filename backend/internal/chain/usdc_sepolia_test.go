//go:build sepolia

package chain

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Proves the real USDC contract accepts our digest and encoding: a valid
// authorization from an unfunded key gets as far as the balance check, and
// a tampered one fails the signature check.
func TestSepolia_SimulateTransfer_ZeroBalanceKey(t *testing.T) {
	rpcURL := os.Getenv("SEPOLIA_RPC_URL")
	if rpcURL == "" {
		t.Skip("SEPOLIA_RPC_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		t.Fatal("dialing RPC failed")
	}
	defer client.Close()

	key := newKey(t)
	a := testAuthorization(crypto.PubkeyToAddress(key.PublicKey))
	a.Value = big.NewInt(1)
	a.ValidBefore = big.NewInt(time.Now().Add(time.Hour).Unix())
	if _, err := cryptorand.Read(a.Nonce[:]); err != nil {
		t.Fatal(err)
	}
	sig := sign(t, key, a.Digest(sepoliaUSDCDomain))
	usdc := NewUSDC(client, sepoliaUSDC)

	assertRevert(t, usdc.SimulateTransfer(ctx, a, sig), "exceeds balance")

	tampered := append([]byte(nil), sig...)
	tampered[10] ^= 0xff
	assertRevert(t, usdc.SimulateTransfer(ctx, a, tampered), "invalid signature")
}

func assertRevert(t *testing.T, err error, want string) {
	t.Helper()
	var re *RevertError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want a revert containing %q", err, want)
	}
	if !strings.Contains(re.Reason, want) {
		t.Fatalf("revert reason %q, want it to contain %q", re.Reason, want)
	}
}
