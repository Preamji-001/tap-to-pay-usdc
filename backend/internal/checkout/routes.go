package checkout

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

const (
	// routeBalanceTTL: a page polling routes re-reads each chain at most this
	// often per payer, so the public endpoint can't be used to hammer RPCs.
	routeBalanceTTL = 5 * time.Second
	// routeTimeout bounds each chain's balance read. A slow chain shows up as
	// unknown instead of holding up the answer.
	routeTimeout = 4 * time.Second
)

// Route is one chain a checkout could be paid on.
type Route struct {
	Network chain.Network
	// Balance is the payer's USDC there, or nil when the chain's RPC didn't
	// answer: unknown, which is not the same as zero.
	Balance    *big.Int
	Sufficient bool
}

// Routes is every chain a checkout can be paid on, and the one to suggest.
type Routes struct {
	Amount *big.Int // the checkout's amount, in base units
	Routes []Route  // registry order
	// Recommended is the chain to pay on, or 0 when no chain is known to
	// hold enough.
	Recommended int64
}

// Routes reads payer's USDC on every chain checkout id can still be paid on,
// in parallel, and recommends one:
//
//  1. preferred, if it holds enough. It is the customer's own choice, or the
//     chain their wallet is already on, which saves a network-switch prompt.
//  2. Otherwise the chain that is cheapest for the relayer (lowest FeeRank)
//     among those holding enough.
//
// A preferred chain this server doesn't enable is ignored. Nothing is bound
// here: the customer can still sign for any chain, and the chain is fixed
// only when an authorization wins.
func (s *Service) Routes(ctx context.Context, id string, payer common.Address, preferred int64) (*Routes, error) {
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
	if !s.now().Before(c.ExpiresAt.Add(-authorizeSafetyMargin)) {
		return nil, ErrExpired
	}

	nets := s.p.Networks.Networks()
	if c.Bound() {
		n, err := s.NetworkFor(c, 0)
		if err != nil {
			return nil, err
		}
		nets = []chain.Network{n}
	}

	out := &Routes{Amount: new(big.Int).Set(c.AmountBaseUnits), Routes: make([]Route, len(nets))}
	var wg sync.WaitGroup
	for i, n := range nets {
		out.Routes[i].Network = n
		rpc, ok := s.chains[n.ChainID]
		if !ok {
			continue // unknown
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			bal, err := s.balances.get(ctx, rpc, n.ChainID, payer)
			if err != nil {
				return // unknown; the next call retries
			}
			out.Routes[i].Balance = bal
			out.Routes[i].Sufficient = bal.Cmp(c.AmountBaseUnits) >= 0
		}()
	}
	wg.Wait()

	var best *Route
	for i := range out.Routes {
		r := &out.Routes[i]
		if !r.Sufficient {
			continue
		}
		if r.Network.ChainID == preferred {
			best = r
			break
		}
		if best == nil || r.Network.FeeRank < best.Network.FeeRank {
			best = r
		}
	}
	if best != nil {
		out.Recommended = best.Network.ChainID
	}
	return out, nil
}

type balanceKey struct {
	chainID int64
	payer   common.Address
}

type balanceEntry struct {
	balance *big.Int
	at      time.Time
}

// balanceCache keeps each (chain, payer) balance for routeBalanceTTL. Failed
// reads are not cached.
type balanceCache struct {
	now     func() time.Time
	mu      sync.Mutex
	entries map[balanceKey]balanceEntry
}

func newBalanceCache(now func() time.Time) *balanceCache {
	return &balanceCache{now: now, entries: map[balanceKey]balanceEntry{}}
}

func (b *balanceCache) get(ctx context.Context, rpc Chain, chainID int64, payer common.Address) (*big.Int, error) {
	key := balanceKey{chainID, payer}
	b.mu.Lock()
	e, ok := b.entries[key]
	b.mu.Unlock()
	if ok && b.now().Sub(e.at) < routeBalanceTTL {
		return new(big.Int).Set(e.balance), nil
	}

	rctx, cancel := context.WithTimeout(ctx, routeTimeout)
	defer cancel()
	bal, err := rpc.BalanceOf(rctx, payer)
	if err != nil {
		return nil, fmt.Errorf("reading balance on chain %d: %w", chainID, err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	// Drop stale entries so the map stays as small as the live traffic.
	for k, old := range b.entries {
		if now.Sub(old.at) >= routeBalanceTTL {
			delete(b.entries, k)
		}
	}
	b.entries[key] = balanceEntry{balance: new(big.Int).Set(bal), at: now}
	return bal, nil
}
