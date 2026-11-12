package chain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// Reader is the read-only slice of an RPC client that startup needs.
// *ethclient.Client satisfies it.
type Reader interface {
	ChainID(ctx context.Context) (*big.Int, error)
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// TokenParams is what we expect the configured token to be.
type TokenParams struct {
	ChainID int64
	Address common.Address
	Name    string
	Version string
}

// selector of DOMAIN_SEPARATOR()
var domainSeparatorSelector = []byte{0x36, 0x44, 0xe5, 0x15}

// CheckToken confirms the RPC is on the expected chain, the token address
// has code, and the token's on-chain DOMAIN_SEPARATOR() equals the one we
// compute from our config. A mismatch means every signature would fail.
func CheckToken(ctx context.Context, r Reader, p TokenParams) error {
	id, err := r.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("reading chain id: %w", redactURL(err))
	}
	if id.Cmp(big.NewInt(p.ChainID)) != 0 {
		return fmt.Errorf("chain id mismatch: RPC is on %s, config expects %d", id, p.ChainID)
	}

	code, err := r.CodeAt(ctx, p.Address, nil)
	if err != nil {
		return fmt.Errorf("reading token code: %w", redactURL(err))
	}
	if len(code) == 0 {
		return fmt.Errorf("token %s has no code on chain %d", p.Address.Hex(), p.ChainID)
	}

	addr := p.Address
	got, err := r.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: domainSeparatorSelector}, nil)
	if err != nil {
		return fmt.Errorf("calling DOMAIN_SEPARATOR(): %w", redactURL(err))
	}
	want := DomainSeparator(p.Name, p.Version, p.ChainID, p.Address)
	if !bytes.Equal(got, want.Bytes()) {
		return fmt.Errorf("domain separator mismatch: token returns 0x%x, config (name %q, version %q) gives %s",
			got, p.Name, p.Version, want.Hex())
	}
	return nil
}

// redactURL drops the request URL from an HTTP transport error. RPC URLs
// usually embed a provider API key, and these errors end up in logs.
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s request to RPC failed: %w", ue.Op, ue.Err)
	}
	return err
}
