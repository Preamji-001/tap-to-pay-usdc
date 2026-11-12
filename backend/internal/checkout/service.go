package checkout

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
	"github.com/Preamji-001/crypto-pos-base/internal/money"
)

// USDC has 6 decimals (checked on-chain).
const tokenDecimals = 6

const maxReferenceLen = 64

// "co_" + base64url (no padding) of 16 random bytes.
var idPattern = regexp.MustCompile(`^co_[A-Za-z0-9_-]{22}$`)

// ValidID reports whether id has the shape of a checkout id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Params are the trusted, server-side values every checkout gets.
type Params struct {
	Merchant common.Address
	// Networks are the enabled chains. A checkout can be paid on any of
	// them; the primary one is used when the customer doesn't choose.
	Networks        *chain.Registry
	TTL             time.Duration
	MaxAmount       *big.Int // inclusive, in base units
	FrontendBaseURL string
}

// CreateRequest is everything a merchant may choose. Merchant, token and
// chain are deliberately absent: they come only from Params.
type CreateRequest struct {
	Amount            string // decimal, e.g. "12.50"
	MerchantReference string // optional, the merchant's own order id
}

// Chains holds the reads for every enabled network, keyed by chain ID. Each
// checkout is checked against the chain it was created on, never another.
type Chains map[int64]Chain

type Service struct {
	repo     Repository
	p        Params
	now      func() time.Time
	chains   Chains
	relayer  Relayer
	balances *balanceCache
}

func NewService(repo Repository, p Params, now func() time.Time, chains Chains, relayer Relayer) *Service {
	return &Service{repo: repo, p: p, now: now, chains: chains, relayer: relayer, balances: newBalanceCache(now)}
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (*Checkout, error) {
	amount, err := money.ParseDecimal(req.Amount, tokenDecimals)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidAmount, err)
	}
	if amount.Cmp(s.p.MaxAmount) > 0 {
		return nil, ErrAmountTooLarge
	}
	if !validReference(req.MerchantReference) {
		return nil, ErrInvalidReference
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// The chain isn't known yet: the customer picks it when paying. Record
	// every chain's head now, so a later search for this checkout's payment
	// can start there on whichever chain it lands.
	starts, err := s.heads(ctx)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	expires := now.Add(s.p.TTL)
	c := &Checkout{
		ID:                id,
		AmountBaseUnits:   amount,
		Merchant:          s.p.Merchant,
		Nonce:             nonce,
		ValidAfter:        0,
		ValidBefore:       expires.Unix(),
		StartBlocks:       starts,
		MerchantReference: req.MerchantReference,
		Status:            StatusPending,
		CreatedAt:         now,
		ExpiresAt:         expires,
		UpdatedAt:         now,
	}
	if err := s.repo.Insert(ctx, c); err != nil {
		return nil, fmt.Errorf("storing checkout: %w", err)
	}
	return c, nil
}

// Get returns the checkout's current state. A pending checkout at or past
// its expiry is reported as expired (stage 1 does not write that back).
func (s *Service) Get(ctx context.Context, id string) (*Checkout, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status == StatusPending && !s.now().Before(c.ExpiresAt) {
		c.Status = StatusExpired
	}
	return c, nil
}

// headTimeout bounds each chain's head read when a checkout is created.
const headTimeout = 5 * time.Second

// heads reads every enabled chain's head in parallel. A chain that doesn't
// answer is left out (its log searches fall back to a fixed lookback); only
// when no chain answers does creating a checkout fail.
func (s *Service) heads(ctx context.Context) (map[int64]uint64, error) {
	nets := s.p.Networks.Networks()
	blocks := make([]uint64, len(nets))
	errs := make([]error, len(nets))
	var wg sync.WaitGroup
	for i, n := range nets {
		rpc, ok := s.chains[n.ChainID]
		if !ok {
			errs[i] = fmt.Errorf("%s: no chain reads wired", n.Key)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			hctx, cancel := context.WithTimeout(ctx, headTimeout)
			defer cancel()
			blocks[i], errs[i] = rpc.BlockNumber(hctx)
		}()
	}
	wg.Wait()

	starts := make(map[int64]uint64, len(nets))
	for i, n := range nets {
		if errs[i] != nil {
			slog.Warn("create: chain head unavailable; its payments will be searched with a fixed lookback",
				"network", n.Key, "err", errs[i])
			continue
		}
		starts[n.ChainID] = blocks[i]
	}
	if len(starts) == 0 {
		return nil, fmt.Errorf("%w: %w", ErrChainUnavailable, errors.Join(errs...))
	}
	return starts, nil
}

// NetworkFor resolves which chain a request about c is for, and so which
// EIP-712 domain applies. chainID 0 means the default: the bound chain, or
// the primary network while c is unbound.
//
//   - A bound checkout only ever answers for its own chain (ErrChainMismatch
//     otherwise). Its chain must still be enabled, and its stored token must
//     be that chain's USDC (ErrUnsupportedChain otherwise).
//   - An unbound checkout answers for any enabled chain (ErrUnknownChain
//     otherwise). Only a chain ID from the registry is accepted, never a
//     token address: the server alone decides which contract is paid.
func (s *Service) NetworkFor(c *Checkout, chainID int64) (chain.Network, error) {
	if c.Bound() {
		if chainID != 0 && chainID != c.ChainID {
			return chain.Network{}, fmt.Errorf("%w: bound to chain %d, not %d", ErrChainMismatch, c.ChainID, chainID)
		}
		n, ok := s.p.Networks.ByID(c.ChainID)
		if !ok {
			return chain.Network{}, fmt.Errorf("%w: chain %d is not enabled", ErrUnsupportedChain, c.ChainID)
		}
		if c.Token != n.USDC {
			return chain.Network{}, fmt.Errorf("%w: token %s is not %s's USDC", ErrUnsupportedChain, c.Token.Hex(), n.Key)
		}
		return n, nil
	}
	if chainID == 0 {
		return s.p.Networks.Primary(), nil
	}
	n, ok := s.p.Networks.ByID(chainID)
	if !ok {
		return chain.Network{}, fmt.Errorf("%w: %d", ErrUnknownChain, chainID)
	}
	return n, nil
}

// chainOf returns the reads for n.
func (s *Service) chainOf(n chain.Network) (Chain, error) {
	rpc, ok := s.chains[n.ChainID]
	if !ok {
		return nil, fmt.Errorf("%w: no chain reads wired for %s", ErrUnsupportedChain, n.Key)
	}
	return rpc, nil
}

// Domain is the EIP-712 domain c's authorization is signed under on chainID
// (0: the default, see NetworkFor).
func (s *Service) Domain(c *Checkout, chainID int64) (chain.TokenParams, error) {
	n, err := s.NetworkFor(c, chainID)
	if err != nil {
		return chain.TokenParams{}, err
	}
	return n.TokenParams(), nil
}

// DomainSeparator is the separator of Domain(c, chainID).
func (s *Service) DomainSeparator(c *Checkout, chainID int64) (common.Hash, error) {
	n, err := s.NetworkFor(c, chainID)
	if err != nil {
		return common.Hash{}, err
	}
	return n.DomainSeparator(), nil
}

func (s *Service) CheckoutURL(id string) string {
	return strings.TrimRight(s.p.FrontendBaseURL, "/") + "/checkout/" + id
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	return "co_" + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func validReference(ref string) bool {
	if len(ref) > maxReferenceLen {
		return false
	}
	for i := 0; i < len(ref); i++ {
		if ref[i] < 0x20 || ref[i] > 0x7e {
			return false
		}
	}
	return true
}
