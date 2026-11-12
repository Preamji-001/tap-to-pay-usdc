package store

import (
	"context"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
)

var (
	sepoliaBinding = checkout.Binding{ChainID: 11155111, Token: common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")}
	baseBinding    = checkout.Binding{ChainID: 84532, Token: common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e")}
)

// unboundCheckout is how Create stores a checkout since L4: no chain yet,
// and every chain's head at creation.
func unboundCheckout(id string, nonceByte byte) *checkout.Checkout {
	c := sampleCheckout(id, nonceByte)
	c.ChainID, c.Token = 0, common.Address{}
	c.StartBlocks = map[int64]uint64{11155111: 9_000_000, 84532: 33_000_000}
	return c
}

func TestUnbound_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := unboundCheckout("co_unboundRoundTripAAAAA", 40)
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bound() || got.Token != (common.Address{}) {
		t.Fatalf("unbound checkout came back bound: chain %d token %s", got.ChainID, got.Token.Hex())
	}
	if len(got.StartBlocks) != 2 || got.StartBlocks[84532] != 33_000_000 || got.StartBlocks[11155111] != 9_000_000 {
		t.Fatalf("start blocks = %v", got.StartBlocks)
	}
	var chainNull, tokenNull bool
	if err := s.pool.QueryRow(ctx, "SELECT chain_id IS NULL, token IS NULL FROM checkouts WHERE id = $1", c.ID).
		Scan(&chainNull, &tokenNull); err != nil || !chainNull || !tokenNull {
		t.Fatalf("stored chain_id/token NULL = %v/%v (%v), want both NULL", chainNull, tokenNull, err)
	}
}

func TestUnbound_ConstraintsHold(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	paid := unboundCheckout("co_unboundButPaidAAAAAAA", 41)
	paid.Status = checkout.StatusSubmitting
	if err := s.Insert(ctx, paid); err == nil {
		t.Error("stored a submitting checkout with no chain")
	}

	c := sampleCheckout("co_chainWithoutTokenAAAA", 42)
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE checkouts SET token = NULL WHERE id = $1", c.ID); err == nil {
		t.Error("stored a chain without its token")
	}
	if _, err := s.pool.Exec(ctx, "UPDATE checkouts SET chain_id = NULL WHERE id = $1", c.ID); err == nil {
		t.Error("stored a token without its chain")
	}
}

func TestMarkSubmitting_BindsTheChain(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := unboundCheckout("co_bindOnAuthorizeAAAAAA", 43)
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	b := baseBinding
	b.CreatedBlock = c.StartBlocks[84532]
	payer := common.HexToAddress("0xaa")
	if ok, err := s.MarkSubmitting(ctx, c.ID, b, payer, []byte{1}, c.CreatedAt); !ok || err != nil {
		t.Fatalf("MarkSubmitting = %v, %v", ok, err)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != checkout.StatusSubmitting || got.ChainID != 84532 || got.Token != baseBinding.Token || got.CreatedBlock != 33_000_000 {
		t.Fatalf("after binding: status %s chain %d token %s block %d", got.Status, got.ChainID, got.Token.Hex(), got.CreatedBlock)
	}
}

// A checkout bound before L4 (at creation) keeps its chain and start block.
func TestMarkSubmitting_BoundRow_OnlyItsOwnChain(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := sampleCheckout("co_legacyBoundAAAAAAAAAA", 44)
	c.CreatedBlock = 9_000_123
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.MarkSubmitting(ctx, c.ID, baseBinding, common.HexToAddress("0xaa"), []byte{1}, c.CreatedAt); ok || err != nil {
		t.Fatalf("Base authorization on a Sepolia-bound checkout = %v, %v; want false", ok, err)
	}
	same := sepoliaBinding
	same.CreatedBlock = 1 // must not overwrite the stored start
	if ok, err := s.MarkSubmitting(ctx, c.ID, same, common.HexToAddress("0xaa"), []byte{1}, c.CreatedAt); !ok || err != nil {
		t.Fatalf("Sepolia authorization = %v, %v; want true", ok, err)
	}
	if got, _ := s.Get(ctx, c.ID); got.ChainID != 11155111 || got.CreatedBlock != 9_000_123 {
		t.Fatalf("chain %d block %d, want Sepolia and 9000123", got.ChainID, got.CreatedBlock)
	}
}

// L4 done-when: of authorizations racing on two chains, exactly one wins, and
// the checkout ends up bound to the winner's chain.
func TestMarkSubmitting_RaceAcrossChains_ExactlyOneWins(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := unboundCheckout("co_raceAcrossChainsAAAAA", 45)
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []checkout.Binding
	)
	for i := range 20 {
		b := sepoliaBinding
		if i%2 == 1 {
			b = baseBinding
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.MarkSubmitting(ctx, c.ID, b, common.BigToAddress(big.NewInt(int64(i+1))), []byte{byte(i)}, time.Now())
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				winners = append(winners, b)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("%d winners, want exactly 1", len(winners))
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChainID != winners[0].ChainID || got.Token != winners[0].Token {
		t.Fatalf("bound to chain %d, but the winner was chain %d", got.ChainID, winners[0].ChainID)
	}
}

func TestTransition_BindsAnUnboundCheckout(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := unboundCheckout("co_bindOnSettleAAAAAAAAA", 46)
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	payer, hash, block := common.HexToAddress("0xbb"), common.HexToHash("0x02"), uint64(33_000_050)
	b := baseBinding
	b.CreatedBlock = 33_000_000
	if ok, err := s.Transition(ctx, c.ID, checkout.StatusPending, checkout.Update{
		Status: checkout.StatusConfirmed, Payer: &payer, TxHash: &hash, ConfirmedBlock: &block, Bind: &b, At: c.CreatedAt,
	}); !ok || err != nil {
		t.Fatalf("Transition = %v, %v", ok, err)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != checkout.StatusConfirmed || got.ChainID != 84532 || got.Token != baseBinding.Token || got.CreatedBlock != 33_000_000 {
		t.Fatalf("after settle: %+v", got)
	}
}

func TestTransition_NeverRebindsToAnotherChain(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := sampleCheckout("co_noRebindAAAAAAAAAAAAA", 47) // bound to Sepolia
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	b := baseBinding
	if ok, err := s.Transition(ctx, c.ID, checkout.StatusPending, checkout.Update{Status: checkout.StatusConfirmed, Bind: &b, At: c.CreatedAt}); ok || err != nil {
		t.Fatalf("rebinding Transition = %v, %v; want false", ok, err)
	}
	// Expiring without a binding still works on an unbound row.
	u := unboundCheckout("co_expireUnboundAAAAAAAA", 48)
	if err := s.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Transition(ctx, u.ID, checkout.StatusPending, checkout.Update{Status: checkout.StatusExpired, At: u.CreatedAt}); !ok || err != nil {
		t.Fatalf("expire unbound = %v, %v", ok, err)
	}
}
