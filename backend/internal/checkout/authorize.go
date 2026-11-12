package checkout

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

// authorizeSafetyMargin leaves the relayer time to land the transaction
// before the authorization's validBefore.
const authorizeSafetyMargin = 60 * time.Second

var (
	ErrNotPending          = errors.New("checkout is not pending")
	ErrExpired             = errors.New("checkout has expired")
	ErrInvalidSignature    = errors.New("signature does not authorize this checkout")
	ErrInsufficientBalance = errors.New("payer's USDC balance is below the amount")
	ErrSimulationFailed    = errors.New("transfer simulation reverted")
	ErrChainUnavailable    = errors.New("chain RPC unavailable")
	ErrUnsupportedChain    = errors.New("checkout's chain is not supported")
	// ErrUnknownChain: the customer asked for a chain this server doesn't enable.
	ErrUnknownChain = errors.New("chain is not enabled on this server")
	// ErrChainMismatch: the checkout is already bound to a different chain.
	ErrChainMismatch = errors.New("checkout is bound to another chain")
)

// Chain is every read the checkout flow makes: the pre-flight before
// accepting a signature, and the receipts and logs that prove a payment.
// *chain.USDC satisfies it.
type Chain interface {
	AuthorizationUsed(ctx context.Context, payer common.Address, nonce [32]byte) (bool, error)
	BalanceOf(ctx context.Context, a common.Address) (*big.Int, error)
	SimulateTransfer(ctx context.Context, a chain.TransferAuthorization, sig []byte) error
	BlockNumber(ctx context.Context) (uint64, error)
	FindAuthorizationUsed(ctx context.Context, payer *common.Address, nonce [32]byte, fromBlock uint64) (*chain.AuthorizationLog, error)
	Receipt(ctx context.Context, hash common.Hash) (*types.Receipt, error)
	TransactionTo(ctx context.Context, hash common.Hash) (to *common.Address, found bool, err error)
}

// Relayer takes over a checkout that has just become submitting.
type Relayer interface {
	Submit(ctx context.Context, c *Checkout)
}

// NoopRelayer accepts and does nothing: checkouts stay submitting. For tests.
type NoopRelayer struct{}

func (NoopRelayer) Submit(context.Context, *Checkout) {}

// Authorize checks payer's EIP-3009 signature for checkout id on chainID (0:
// the default, see NetworkFor) against the stored checkout and that chain,
// then moves it to submitting and binds it to the chain. Local checks run
// first, so a bad or late request never reaches the RPC. Exactly one caller
// can win the move, even across chains; only the winner hands the checkout
// to the relayer.
func (s *Service) Authorize(ctx context.Context, id string, chainID int64, payer common.Address, sig []byte) (*Checkout, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != StatusPending {
		return nil, ErrNotPending
	}
	now := s.now()
	if !now.Before(c.ExpiresAt.Add(-authorizeSafetyMargin)) {
		return nil, ErrExpired
	}

	// The domain comes from the chain being paid on, so a signature made for
	// any other chain recovers a different address and is rejected. Every
	// RPC read below goes to that same chain.
	network, err := s.NetworkFor(c, chainID)
	if err != nil {
		return nil, err
	}
	rpc, err := s.chainOf(network)
	if err != nil {
		return nil, err
	}
	a := c.Authorization(payer)
	signer, err := chain.RecoverSigner(a.Digest(network.DomainSeparator()), sig)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	if signer != payer {
		return nil, ErrInvalidSignature
	}

	// Already used on chain means our relayer (or someone else) submitted it:
	// accept without relaying again, and let the reconciler find the transfer.
	used, err := rpc.AuthorizationUsed(ctx, payer, c.Nonce)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChainUnavailable, err)
	}
	if !used {
		if err := preflight(ctx, rpc, a, sig); err != nil {
			return nil, err
		}
	}

	bind := Binding{ChainID: network.ChainID, Token: network.USDC, CreatedBlock: c.StartBlocks[network.ChainID]}
	if c.Bound() {
		bind.CreatedBlock = c.CreatedBlock
	}
	won, err := s.repo.MarkSubmitting(ctx, id, bind, payer, sig, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("marking submitting: %w", err)
	}
	if !won {
		return nil, ErrNotPending
	}
	c, err = s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !used {
		s.relayer.Submit(ctx, c)
	}
	return c, nil
}

func preflight(ctx context.Context, rpc Chain, a chain.TransferAuthorization, sig []byte) error {
	bal, err := rpc.BalanceOf(ctx, a.From)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrChainUnavailable, err)
	}
	if bal.Cmp(a.Value) < 0 {
		return ErrInsufficientBalance
	}
	if err := rpc.SimulateTransfer(ctx, a, sig); err != nil {
		var re *chain.RevertError
		if errors.As(err, &re) {
			return fmt.Errorf("%w: %w", ErrSimulationFailed, err)
		}
		return fmt.Errorf("%w: %w", ErrChainUnavailable, err)
	}
	return nil
}
