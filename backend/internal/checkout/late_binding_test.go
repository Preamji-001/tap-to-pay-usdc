package checkout

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

// L4: the chain is chosen when the customer pays, not when the checkout is
// created.

// lateFixture is a fresh, unbound checkout on a server enabling keys (first =
// primary), with separate fake reads per chain.
func lateFixture(t *testing.T, keys ...string) (*authFixture, map[string]*fakeChain) {
	t.Helper()
	p := testParams()
	p.Networks = testRegistry(keys...)
	fakes, chains := map[string]*fakeChain{}, Chains{}
	for i, k := range keys {
		fakes[k] = &fakeChain{head: uint64(1_000 * (i + 1))}
		chains[network(t, k).ChainID] = fakes[k]
	}
	f := newAuthFixtureWith(t, p)
	f.svc = NewService(f.repo, p, func() time.Time { return *f.now }, chains, f.relayer)
	c, err := f.svc.Create(context.Background(), CreateRequest{Amount: "12.50"})
	if err != nil {
		t.Fatal(err)
	}
	f.c = c
	for _, fc := range fakes {
		fc.calls = 0
	}
	return f, fakes
}

func (f *authFixture) sigFor(t *testing.T, key string) []byte {
	return f.signAuth(t, f.c.Authorization(f.payer), network(t, key).DomainSeparator())
}

func TestAuthorize_BindsTheChosenChain(t *testing.T) {
	f, fakes := lateFixture(t, "sepolia", "base-sepolia")
	base := network(t, "base-sepolia")
	if _, err := f.authorizeOn(base.ChainID, f.sigFor(t, "base-sepolia")); err != nil {
		t.Fatalf("Authorize on Base: %v", err)
	}
	got := f.row(t)
	if got.Status != StatusSubmitting || got.ChainID != base.ChainID || got.Token != base.USDC || got.CreatedBlock != 2_000 {
		t.Fatalf("status %s chain %d token %s block %d; want submitting on Base from its start block 2000",
			got.Status, got.ChainID, got.Token.Hex(), got.CreatedBlock)
	}
	if fakes["sepolia"].callCount() != 0 || fakes["base-sepolia"].callCount() == 0 {
		t.Fatalf("reads: Sepolia %d, Base %d; want Base only", fakes["sepolia"].callCount(), fakes["base-sepolia"].callCount())
	}
	if f.relayer.submitted.Load() != 1 {
		t.Fatal("not handed to the relayer")
	}
}

func TestAuthorize_DefaultChainIsPrimary(t *testing.T) {
	f, _ := lateFixture(t, "base-sepolia", "sepolia")
	if _, err := f.authorizeOn(0, f.sigFor(t, "base-sepolia")); err != nil {
		t.Fatalf("Authorize with no chain: %v", err)
	}
	if got := f.row(t); got.ChainID != network(t, "base-sepolia").ChainID {
		t.Fatalf("bound to %d, want the primary Base Sepolia", got.ChainID)
	}
}

// L4 done-when: a chain not in the registry is rejected, before any RPC call.
func TestAuthorize_ChainNotInRegistryRejected(t *testing.T) {
	for _, id := range []int64{1, 8453, 80002} { // mainnet, Base mainnet, Amoy (not enabled here)
		f, fakes := lateFixture(t, "sepolia", "base-sepolia")
		if _, err := f.authorizeOn(id, f.sigFor(t, "sepolia")); !errors.Is(err, ErrUnknownChain) {
			t.Fatalf("chain %d: Authorize = %v, want ErrUnknownChain", id, err)
		}
		if fakes["sepolia"].callCount()+fakes["base-sepolia"].callCount() != 0 || f.row(t).Bound() || f.status(t) != StatusPending {
			t.Fatalf("chain %d: reached an RPC or changed the checkout", id)
		}
	}
}

// L4 done-when: of authorizations racing on two chains, exactly one wins; the
// checkout is bound to the winner's chain and relayed once.
func TestAuthorize_RaceAcrossChains_ExactlyOneWins(t *testing.T) {
	f, _ := lateFixture(t, "sepolia", "base-sepolia")
	keys := []string{"sepolia", "base-sepolia"}
	sigs := map[string][]byte{"sepolia": f.sigFor(t, "sepolia"), "base-sepolia": f.sigFor(t, "base-sepolia")}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		winner []string
	)
	for i := range 20 {
		k := keys[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.authorizeOn(network(t, k).ChainID, sigs[k])
			switch {
			case err == nil:
				mu.Lock()
				winner = append(winner, k)
				mu.Unlock()
			case !errors.Is(err, ErrNotPending):
				t.Errorf("loser on %s: %v, want ErrNotPending", k, err)
			}
		}()
	}
	wg.Wait()
	if len(winner) != 1 {
		t.Fatalf("%d winners, want exactly 1", len(winner))
	}
	if got := f.row(t); got.ChainID != network(t, winner[0]).ChainID {
		t.Fatalf("bound to %d, but %s won", got.ChainID, winner[0])
	}
	if f.relayer.submitted.Load() != 1 {
		t.Fatalf("relayed %d times, want 1", f.relayer.submitted.Load())
	}
}

// A checkout bound before L4 (at creation) only accepts its own chain.
func TestAuthorize_BoundCheckout_OtherChainIsMismatch(t *testing.T) {
	f, _ := lateFixture(t, "sepolia", "base-sepolia")
	sep := network(t, "sepolia")
	row := f.repo.rows[f.c.ID]
	row.ChainID, row.Token, row.CreatedBlock = sep.ChainID, sep.USDC, 77
	f.repo.rows[f.c.ID] = row
	f.c = &row

	base := network(t, "base-sepolia").ChainID
	if _, err := f.authorizeOn(base, f.sigFor(t, "base-sepolia")); !errors.Is(err, ErrChainMismatch) {
		t.Fatalf("Base authorization on a Sepolia checkout = %v, want ErrChainMismatch", err)
	}
	if _, err := f.authorizeOn(0, f.sigFor(t, "sepolia")); err != nil {
		t.Fatalf("default chain on a bound checkout: %v", err)
	}
	if got := f.row(t); got.CreatedBlock != 77 {
		t.Fatalf("CreatedBlock %d, want the stored 77", got.CreatedBlock)
	}
}

// --- Routes ---

func routeFor(t *testing.T, r *Routes, key string) Route {
	t.Helper()
	for _, rt := range r.Routes {
		if rt.Network.Key == key {
			return rt
		}
	}
	t.Fatalf("no route for %s in %+v", key, r.Routes)
	return Route{}
}

func usdc(v int64) *big.Int { return big.NewInt(v * 1_000_000) }

func TestRoutes_Recommendation(t *testing.T) {
	sepID, baseID, amoyID := int64(11155111), int64(84532), int64(80002)
	tests := []struct {
		name      string
		balances  map[string]int64 // whole USDC; the checkout is 12.50
		preferred int64
		want      int64
	}{
		{"preferred chain has enough: no switch prompt", map[string]int64{"sepolia": 20, "base-sepolia": 20, "amoy": 20}, sepID, sepID},
		{"no preference: cheapest for the relayer", map[string]int64{"sepolia": 20, "base-sepolia": 20, "amoy": 20}, 0, baseID},
		{"preferred chain short: cheapest that has enough", map[string]int64{"sepolia": 20, "base-sepolia": 5, "amoy": 20}, baseID, amoyID},
		{"preferred chain not enabled: ignored", map[string]int64{"sepolia": 20, "base-sepolia": 1, "amoy": 1}, 1, sepID},
		{"nowhere enough", map[string]int64{"sepolia": 12, "base-sepolia": 3, "amoy": 0}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, fakes := lateFixture(t, "sepolia", "base-sepolia", "amoy")
			for k, v := range tt.balances {
				fakes[k].balance = usdc(v)
			}
			r, err := f.svc.Routes(context.Background(), f.c.ID, f.payer, tt.preferred)
			if err != nil {
				t.Fatal(err)
			}
			if r.Recommended != tt.want {
				t.Fatalf("recommended %d, want %d (%+v)", r.Recommended, tt.want, r.Routes)
			}
			if len(r.Routes) != 3 || r.Amount.Cmp(big.NewInt(12_500_000)) != 0 {
				t.Fatalf("%d routes, amount %s", len(r.Routes), r.Amount)
			}
		})
	}
}

func TestRoutes_ExactAmountIsEnough(t *testing.T) {
	f, fakes := lateFixture(t, "sepolia")
	fakes["sepolia"].balance = big.NewInt(12_500_000)
	r, err := f.svc.Routes(context.Background(), f.c.ID, f.payer, 0)
	if err != nil || !routeFor(t, r, "sepolia").Sufficient {
		t.Fatalf("exactly the amount is not sufficient (%v)", err)
	}
}

func TestRoutes_DownChainIsUnknownNotZero(t *testing.T) {
	f, fakes := lateFixture(t, "sepolia", "base-sepolia")
	fakes["base-sepolia"].err = errors.New("base down")
	fakes["sepolia"].balance = usdc(20)
	r, err := f.svc.Routes(context.Background(), f.c.ID, f.payer, 0)
	if err != nil {
		t.Fatal(err)
	}
	if b := routeFor(t, r, "base-sepolia"); b.Balance != nil || b.Sufficient {
		t.Fatalf("down chain: %+v, want unknown balance", b)
	}
	if r.Recommended != 11155111 {
		t.Fatalf("recommended %d, want Sepolia", r.Recommended)
	}
}

func TestRoutes_BalancesCachedBriefly(t *testing.T) {
	f, fakes := lateFixture(t, "sepolia", "base-sepolia")
	ctx := context.Background()
	for range 3 {
		if _, err := f.svc.Routes(ctx, f.c.ID, f.payer, 0); err != nil {
			t.Fatal(err)
		}
	}
	if n := fakes["sepolia"].callCount(); n != 1 {
		t.Fatalf("%d Sepolia reads for 3 calls within the TTL, want 1", n)
	}
	*f.now = f.now.Add(routeBalanceTTL)
	if _, err := f.svc.Routes(ctx, f.c.ID, f.payer, 0); err != nil {
		t.Fatal(err)
	}
	if n := fakes["sepolia"].callCount(); n != 2 {
		t.Fatalf("%d Sepolia reads after the TTL, want 2", n)
	}

	// A failed read isn't cached: the next call tries again.
	fakes["base-sepolia"].err = errors.New("blip")
	*f.now = f.now.Add(routeBalanceTTL)
	f.svc.Routes(ctx, f.c.ID, f.payer, 0)
	fakes["base-sepolia"].err = nil
	before := fakes["base-sepolia"].callCount()
	r, _ := f.svc.Routes(ctx, f.c.ID, f.payer, 0)
	if fakes["base-sepolia"].callCount() != before+1 || routeFor(t, r, "base-sepolia").Balance == nil {
		t.Fatal("a failed balance read was cached")
	}
}

func TestRoutes_BindsNothing_OnlyForPayableCheckouts(t *testing.T) {
	f, _ := lateFixture(t, "sepolia", "base-sepolia")
	ctx := context.Background()
	if _, err := f.svc.Routes(ctx, f.c.ID, f.payer, 84532); err != nil {
		t.Fatal(err)
	}
	if f.row(t).Bound() {
		t.Fatal("Routes bound the chain")
	}
	if _, err := f.svc.Routes(ctx, "co_nopeAAAAAAAAAAAAAAAAA", f.payer, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	*f.now = f.c.ExpiresAt.Add(-authorizeSafetyMargin)
	if _, err := f.svc.Routes(ctx, f.c.ID, f.payer, 0); !errors.Is(err, ErrExpired) {
		t.Fatalf("too late to pay: %v, want ErrExpired", err)
	}
	*f.now = testNow
	if _, err := f.authorizeOn(0, f.sigFor(t, "sepolia")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Routes(ctx, f.c.ID, f.payer, 0); !errors.Is(err, ErrNotPending) {
		t.Fatalf("after authorization: %v, want ErrNotPending", err)
	}
}

func TestRoutes_BoundCheckout_OnlyItsChain(t *testing.T) {
	f, _ := lateFixture(t, "sepolia", "base-sepolia")
	base := network(t, "base-sepolia")
	row := f.repo.rows[f.c.ID]
	row.ChainID, row.Token = base.ChainID, base.USDC
	f.repo.rows[f.c.ID] = row
	r, err := f.svc.Routes(context.Background(), f.c.ID, f.payer, 11155111)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Routes) != 1 || r.Routes[0].Network.ChainID != base.ChainID || r.Recommended != base.ChainID {
		t.Fatalf("routes %+v, recommended %d; want Base only", r.Routes, r.Recommended)
	}
}

// --- Reconciler: checkouts nobody authorized ---

// unbound stores a pending checkout with no chain, created at testNow.
func (l *laneSetup) unbound(t *testing.T, id string) *Checkout {
	t.Helper()
	c := Checkout{
		ID: id, Merchant: testMerchant, AmountBaseUnits: big.NewInt(1_500_000), Nonce: [32]byte{0xEE, byte(len(id))},
		ValidBefore: testNow.Add(15 * time.Minute).Unix(), ExpiresAt: testNow.Add(15 * time.Minute),
		StartBlocks: map[int64]uint64{11155111: 900, 84532: 4_900},
		Status:      StatusPending, UpdatedAt: testNow,
	}
	l.repo.rows[id] = c
	return &c
}

// paidOn makes c look paid by payer on the chain behind fc, in block.
func paidOn(t *testing.T, fc *fakeChain, c *Checkout, key string, payer common.Address, block uint64) {
	t.Helper()
	paid := *c
	paid.Token = network(t, key).USDC
	h := common.HexToHash("0xbeef")
	fc.authLog = &chain.AuthorizationLog{Payer: payer, TxHash: h, Block: block}
	fc.receipts[h] = receiptFor(&paid, payer, block)
}

func TestReconcileUnbound_NotExpired_NoReads(t *testing.T) {
	l := newLaneSetup(t)
	c := l.unbound(t, "co_unboundFreshAAAAAAAAA")
	l.rec.Step(context.Background())
	if l.status(t, c.ID) != StatusPending || l.sepChain.callCount()+l.baseChain.callCount() != 0 {
		t.Fatal("touched an unbound checkout before its expiry")
	}
}

func TestReconcileUnbound_PaidOnBase_ConfirmedAndBound(t *testing.T) {
	l := newLaneSetup(t)
	c := l.unbound(t, "co_unboundPaidOnBaseAAAA")
	outsider := common.HexToAddress("0xcafe")
	paidOn(t, l.baseChain, c, "base-sepolia", outsider, 5_000)
	l.now = c.ExpiresAt

	l.rec.Step(context.Background())
	got, _ := l.repo.Get(context.Background(), c.ID)
	base := network(t, "base-sepolia")
	if got.Status != StatusConfirmed || got.ChainID != base.ChainID || got.Token != base.USDC || *got.Payer != outsider || got.CreatedBlock != 4_900 {
		t.Fatalf("got status %s chain %d token %s block %d", got.Status, got.ChainID, got.Token.Hex(), got.CreatedBlock)
	}
}

func TestReconcileUnbound_NowhereFound_Expired(t *testing.T) {
	l := newLaneSetup(t)
	c := l.unbound(t, "co_unboundNeverPaidAAAAA")
	l.now = c.ExpiresAt
	l.rec.Step(context.Background())
	if got := l.status(t, c.ID); got != StatusExpired {
		t.Fatalf("status %s, want expired", got)
	}
	if l.sepChain.callCount() == 0 || l.baseChain.callCount() == 0 {
		t.Fatal("expired without searching every chain")
	}
}

// A chain that can't be searched means a payment can't be ruled out yet.
func TestReconcileUnbound_ChainDown_StaysPending(t *testing.T) {
	l := newLaneSetup(t)
	c := l.unbound(t, "co_unboundChainDownAAAAA")
	l.sepChain.err = errors.New("sepolia down")
	l.now = c.ExpiresAt
	l.rec.Step(context.Background())
	if got := l.status(t, c.ID); got != StatusPending {
		t.Fatalf("status %s, want pending until every chain was searched", got)
	}
	l.sepChain.err = nil
	l.rec.Step(context.Background())
	if got := l.status(t, c.ID); got != StatusExpired {
		t.Fatalf("status %s once Sepolia answers, want expired", got)
	}
}

// Someone else using the same nonce value on Sepolia must not hide the real
// payment on Base.
func TestReconcileUnbound_UnverifiedLogElsewhere_KeepsLooking(t *testing.T) {
	l := newLaneSetup(t)
	c := l.unbound(t, "co_unboundDecoyAAAAAAAAA")
	decoy := common.HexToHash("0xdec0")
	l.sepChain.authLog = &chain.AuthorizationLog{Payer: common.HexToAddress("0xbad"), TxHash: decoy, Block: 950}
	l.sepChain.receipts[decoy] = &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(950)}
	paidOn(t, l.baseChain, c, "base-sepolia", common.HexToAddress("0xcafe"), 5_000)
	l.now = c.ExpiresAt

	l.rec.Step(context.Background())
	got, _ := l.repo.Get(context.Background(), c.ID)
	if got.Status != StatusConfirmed || got.ChainID != 84532 {
		t.Fatalf("status %s on chain %d, want confirmed on Base", got.Status, got.ChainID)
	}
}

func TestReconcileUnbound_SearchStartsAtTheChainsStartBlock(t *testing.T) {
	l := newLaneSetup(t)
	c := l.unbound(t, "co_unboundOldLogAAAAAAAA")
	// A log for this nonce from before the checkout existed can't be its payment.
	paidOn(t, l.baseChain, c, "base-sepolia", common.HexToAddress("0xcafe"), 4_000)
	l.now = c.ExpiresAt
	l.rec.Step(context.Background())
	if got := l.status(t, c.ID); got != StatusExpired {
		t.Fatalf("status %s: a log before the start block was taken", got)
	}
}
