// Package checkout holds the checkout domain: its fields, states and rules.
// The server alone decides every field a payment depends on.
package checkout

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
	"github.com/Preamji-001/crypto-pos-base/internal/money"
)

type Status string

const (
	StatusPending    Status = "pending"    // waiting for the customer's signature
	StatusSubmitting Status = "submitting" // signature accepted; relayer owns it
	StatusSubmitted  Status = "submitted"  // tx hash stored; waiting for the receipt
	StatusConfirmed  Status = "confirmed"  // receipt and events verified
	StatusFailed     Status = "failed"     // mined revert or verification mismatch
	StatusExpired    Status = "expired"    // no valid signature before ExpiresAt
)

// Terminal reports whether no further transition can happen.
func (s Status) Terminal() bool {
	return s == StatusConfirmed || s == StatusFailed || s == StatusExpired
}

var (
	ErrNotFound = errors.New("checkout not found")
	// ErrInvalidAmount wraps money.ErrInvalidAmount, so errors.Is matches either.
	ErrInvalidAmount    = fmt.Errorf("checkout: %w", money.ErrInvalidAmount)
	ErrAmountTooLarge   = errors.New("amount exceeds the maximum")
	ErrInvalidReference = errors.New("merchant reference must be at most 64 printable ASCII characters")
)

type Checkout struct {
	ID              string
	AmountBaseUnits *big.Int
	Merchant        common.Address
	// Token and ChainID are zero until the customer authorizes on a chain
	// (late binding). Once set they never change.
	Token   common.Address
	ChainID int64
	// Nonce is the EIP-3009 authorization nonce. It ties the customer's
	// signature, and the on-chain AuthorizationUsed event, to this checkout.
	Nonce       [32]byte
	ValidAfter  int64 // unix seconds
	ValidBefore int64 // unix seconds; equals ExpiresAt
	// StartBlocks is every enabled chain's head when the checkout was
	// created. A chain missing here was unreachable at that moment.
	StartBlocks map[int64]uint64
	// CreatedBlock is the bound chain's start block, where log searches
	// start. It is set when the chain is bound; 0 means unknown.
	CreatedBlock      uint64
	MerchantReference string
	Status            Status

	Payer          *common.Address
	Signature      []byte
	TxHash         *common.Hash
	FailureReason  string
	ConfirmedBlock *uint64
	RelayerNonce   *uint64

	CreatedAt time.Time
	ExpiresAt time.Time
	UpdatedAt time.Time
}

// Bound reports whether the checkout's chain is fixed.
func (c *Checkout) Bound() bool { return c.ChainID != 0 }

// Binding fixes a checkout to one chain and its USDC.
type Binding struct {
	ChainID      int64
	Token        common.Address
	CreatedBlock uint64
}

// Authorization is the EIP-3009 message this checkout must be signed as.
// Every field except From comes from the stored checkout.
func (c *Checkout) Authorization(from common.Address) chain.TransferAuthorization {
	return chain.TransferAuthorization{
		From:        from,
		To:          c.Merchant,
		Value:       new(big.Int).Set(c.AmountBaseUnits),
		ValidAfter:  big.NewInt(c.ValidAfter),
		ValidBefore: big.NewInt(c.ValidBefore),
		Nonce:       c.Nonce,
	}
}

// Repository stores checkouts. Get returns ErrNotFound for a missing id.
type Repository interface {
	Insert(ctx context.Context, c *Checkout) error
	Get(ctx context.Context, id string) (*Checkout, error)
	// MarkSubmitting moves a pending checkout to submitting, binds it to b's
	// chain and stores the payer's signature, atomically. It reports false
	// when the checkout is missing, no longer pending, or already bound to
	// another chain, so only one caller can ever win.
	MarkSubmitting(ctx context.Context, id string, b Binding, payer common.Address, sig []byte, at time.Time) (bool, error)
	// Transition applies u only while the checkout is still in status from,
	// and reports whether it did.
	Transition(ctx context.Context, id string, from Status, u Update) (bool, error)
	// ListActive returns every checkout that is not terminal.
	ListActive(ctx context.Context) ([]*Checkout, error)
}

// Update is a status change. Nil pointers and an empty FailureReason leave
// the stored value as it is.
type Update struct {
	Status         Status
	Payer          *common.Address
	TxHash         *common.Hash
	FailureReason  string
	ConfirmedBlock *uint64
	RelayerNonce   *uint64
	// Bind fixes an unbound checkout's chain: a payment for it was found on
	// that chain before anyone authorized it with us.
	Bind *Binding
	At   time.Time
}
