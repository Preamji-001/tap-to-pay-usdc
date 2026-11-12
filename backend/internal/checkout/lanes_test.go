package checkout

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// L3: one relayer key, many chains. Every read, send and verification for a
// checkout goes to the checkout's own chain, and chains don't hold each other up.

// twoChainService serves Sepolia (primary) and Base Sepolia, each with its own
// fake reads, and returns a Base Sepolia checkout the payer can sign.
func twoChainService(t *testing.T) (*authFixture, *fakeChain, *fakeChain) {
	t.Helper()
	sep, base := &fakeChain{}, &fakeChain{}
	p := testParams()
	p.Networks = testRegistry("sepolia", "base-sepolia")
	f := newAuthFixtureWith(t, p)
	f.svc = NewService(f.repo, p, func() time.Time { return *f.now },
		Chains{network(t, "sepolia").ChainID: sep, network(t, "base-sepolia").ChainID: base}, f.relayer)

	bn := network(t, "base-sepolia")
	row := f.repo.rows[f.c.ID]
	row.ChainID, row.Token = bn.ChainID, bn.USDC
	f.repo.rows[f.c.ID] = row
	f.c = &row
	return f, sep, base
}

func TestAuthorize_PreflightReadsTheCheckoutsOwnChain(t *testing.T) {
	f, sep, base := twoChainService(t)
	if _, err := f.authorize(f.validSig(t)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if sep.callCount() != 0 || base.callCount() == 0 {
		t.Fatalf("Base checkout read Sepolia %d times and Base %d times; want only Base", sep.callCount(), base.callCount())
	}
}

func TestAuthorize_BalanceComesFromTheCheckoutsChain(t *testing.T) {
	f, _, base := twoChainService(t)
	base.balance = big.NewInt(1) // plenty on Sepolia, almost nothing on Base
	if _, err := f.authorize(f.validSig(t)); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("Authorize = %v, want ErrInsufficientBalance from Base's balance", err)
	}
	if f.status(t) != StatusPending {
		t.Fatalf("status %s", f.status(t))
	}
}

func TestAuthorize_ChainEnabledButNotWired_Unsupported(t *testing.T) {
	f, _, _ := twoChainService(t)
	sep := &fakeChain{}
	f.svc = NewService(f.repo, f.svc.p, func() time.Time { return *f.now },
		Chains{network(t, "sepolia").ChainID: sep}, f.relayer)
	if _, err := f.authorize(f.validSig(t)); !errors.Is(err, ErrUnsupportedChain) {
		t.Fatalf("Authorize = %v, want ErrUnsupportedChain", err)
	}
	if sep.callCount() != 0 || f.status(t) != StatusPending {
		t.Fatalf("calls %d, status %s", sep.callCount(), f.status(t))
	}
}

// L4: Create binds no chain. It records every chain's head instead, and only
// fails when no chain answers.
func TestCreate_RecordsEveryChainsHead(t *testing.T) {
	sep, base := &fakeChain{head: 111}, &fakeChain{head: 777}
	p := testParams()
	p.Networks = testRegistry("base-sepolia", "sepolia")
	svc := NewService(newMemRepo(), p, func() time.Time { return testNow },
		Chains{network(t, "sepolia").ChainID: sep, network(t, "base-sepolia").ChainID: base}, NoopRelayer{})
	sepID, baseID := network(t, "sepolia").ChainID, network(t, "base-sepolia").ChainID

	c, err := svc.Create(context.Background(), CreateRequest{Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Bound() || len(c.StartBlocks) != 2 || c.StartBlocks[sepID] != 111 || c.StartBlocks[baseID] != 777 {
		t.Fatalf("bound %v, start blocks %v; want unbound with Sepolia 111 and Base 777", c.Bound(), c.StartBlocks)
	}

	base.err = errors.New("base down")
	c, err = svc.Create(context.Background(), CreateRequest{Amount: "1"})
	if err != nil {
		t.Fatalf("one chain down: %v", err)
	}
	if _, ok := c.StartBlocks[baseID]; ok || c.StartBlocks[sepID] != 111 {
		t.Fatalf("start blocks %v; want Sepolia only", c.StartBlocks)
	}

	sep.err = errors.New("sepolia down")
	if _, err := svc.Create(context.Background(), CreateRequest{Amount: "1"}); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("every chain down: %v, want ErrChainUnavailable", err)
	}
}

// laneSetup is a reconciler with a Sepolia lane and a Base Sepolia lane.
type laneSetup struct {
	repo                  *memRepo
	rec                   *Reconciler
	sepChain, baseChain   *fakeChain
	sepSender, baseSender *fakeSender
	now                   time.Time
}

func newLaneSetup(t *testing.T) *laneSetup {
	t.Helper()
	l := &laneSetup{
		repo:      newMemRepo(),
		sepChain:  &fakeChain{head: 1_000, receipts: map[common.Hash]*types.Receipt{}, known: map[common.Hash]common.Address{}},
		baseChain: &fakeChain{head: 5_000, receipts: map[common.Hash]*types.Receipt{}, known: map[common.Hash]common.Address{}},
		sepSender: &fakeSender{nonce: 7}, baseSender: &fakeSender{nonce: 300},
		now: testNow,
	}
	sep, base := network(t, "sepolia"), network(t, "base-sepolia")
	sep.Confirmations, base.Confirmations = 1, 1
	l.rec = NewReconciler(l.repo, []Lane{
		{Network: sep, Chain: l.sepChain, Sender: l.sepSender},
		{Network: base, Chain: l.baseChain, Sender: l.baseSender},
	}, func() time.Time { return l.now })
	return l
}

// accepted stores a checkout on key's chain that the relayer must now send.
func (l *laneSetup) accepted(t *testing.T, id, key string) *Checkout {
	t.Helper()
	n := network(t, key)
	payer := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	c := Checkout{
		ID: id, ChainID: n.ChainID, Token: n.USDC, Merchant: testMerchant,
		AmountBaseUnits: big.NewInt(1_500_000), Nonce: [32]byte{byte(len(id))},
		ValidBefore: testNow.Add(15 * time.Minute).Unix(), ExpiresAt: testNow.Add(15 * time.Minute),
		Status: StatusSubmitting, Payer: &payer, Signature: []byte{1}, UpdatedAt: testNow,
	}
	l.repo.rows[id] = c
	return &c
}

func (l *laneSetup) status(t *testing.T, id string) Status {
	t.Helper()
	c, err := l.repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return c.Status
}

func TestReconcile_EachCheckoutSettlesOnItsOwnChain(t *testing.T) {
	l := newLaneSetup(t)
	sepC := l.accepted(t, "co_onSepoliaAAAAAAAAAAAA", "sepolia")
	baseC := l.accepted(t, "co_onBaseAAAAAAAAAAAAAAA", "base-sepolia")

	l.rec.Step(context.Background())
	if len(l.sepSender.sends) != 1 || len(l.baseSender.sends) != 1 {
		t.Fatalf("sends: Sepolia %d, Base %d; want 1 each", len(l.sepSender.sends), len(l.baseSender.sends))
	}
	sepRow, _ := l.repo.Get(context.Background(), sepC.ID)
	baseRow, _ := l.repo.Get(context.Background(), baseC.ID)
	if *sepRow.RelayerNonce != 7 || *baseRow.RelayerNonce != 300 {
		t.Fatalf("account nonces Sepolia %d, Base %d; each chain must use its own", *sepRow.RelayerNonce, *baseRow.RelayerNonce)
	}

	// Both fake senders hand out hash 0x…01. Each chain only sees its own
	// receipt, so the same hash on two chains can't be confused.
	h := *sepRow.TxHash
	l.sepChain.receipts[h] = receiptFor(sepC, *sepC.Payer, 1_000)
	l.sepChain.known[h] = sepC.Token
	l.baseChain.receipts[h] = receiptFor(baseC, *baseC.Payer, 5_000)
	l.baseChain.known[h] = baseC.Token

	l.rec.Step(context.Background())
	if l.status(t, sepC.ID) != StatusConfirmed || l.status(t, baseC.ID) != StatusConfirmed {
		t.Fatalf("Sepolia %s, Base %s; want both confirmed", l.status(t, sepC.ID), l.status(t, baseC.ID))
	}
}

// A Base receipt carries Base USDC's logs. Shown to the Sepolia lane it
// proves nothing, because the lane only trusts its own token's logs.
func TestReconcile_ReceiptFromAnotherChainsTokenFails(t *testing.T) {
	l := newLaneSetup(t)
	sepC := l.accepted(t, "co_onSepoliaAAAAAAAAAAAA", "sepolia")
	l.rec.Step(context.Background())
	row, _ := l.repo.Get(context.Background(), sepC.ID)

	asBase := *sepC
	asBase.Token = network(t, "base-sepolia").USDC
	l.sepChain.receipts[*row.TxHash] = receiptFor(&asBase, *sepC.Payer, 1_000)
	l.sepChain.known[*row.TxHash] = sepC.Token

	l.rec.Step(context.Background())
	if got := l.status(t, sepC.ID); got != StatusFailed {
		t.Fatalf("status %s, want failed", got)
	}
}

func TestReconcile_DownChainDoesNotStopOthers(t *testing.T) {
	l := newLaneSetup(t)
	l.baseChain.err = errors.New("base RPC down")
	sepC := l.accepted(t, "co_onSepoliaAAAAAAAAAAAA", "sepolia")
	baseC := l.accepted(t, "co_onBaseAAAAAAAAAAAAAAA", "base-sepolia")

	l.rec.Step(context.Background())
	if l.status(t, sepC.ID) != StatusSubmitted || l.status(t, baseC.ID) != StatusSubmitting {
		t.Fatalf("Sepolia %s, Base %s; want Sepolia sent, Base waiting", l.status(t, sepC.ID), l.status(t, baseC.ID))
	}
}

// A chain that hangs must not delay another chain within the same pass.
func TestReconcile_SlowChainDoesNotDelayOthers(t *testing.T) {
	l := newLaneSetup(t)
	l.baseChain.gate = make(chan struct{})
	sepC := l.accepted(t, "co_onSepoliaAAAAAAAAAAAA", "sepolia")
	l.accepted(t, "co_onBaseAAAAAAAAAAAAAAA", "base-sepolia")

	done := make(chan struct{})
	go func() { l.rec.Step(context.Background()); close(done) }()
	defer func() { close(l.baseChain.gate); <-done }()

	deadline := time.Now().Add(2 * time.Second)
	for l.status(t, sepC.ID) != StatusSubmitted {
		if time.Now().After(deadline) {
			t.Fatal("Sepolia checkout waited for the hung Base RPC")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReconcile_ChainWithoutLaneIsLeftAlone(t *testing.T) {
	l := newLaneSetup(t)
	c := l.accepted(t, "co_onAmoyAAAAAAAAAAAAAAA", "amoy")
	l.now = c.ExpiresAt.Add(time.Hour) // even long past its window

	l.rec.Step(context.Background())
	if got := l.status(t, c.ID); got != StatusSubmitting {
		t.Fatalf("status %s, want untouched submitting", got)
	}
	if l.sepChain.callCount()+l.baseChain.callCount() != 0 || len(l.sepSender.sends)+len(l.baseSender.sends) != 0 {
		t.Fatal("another chain's lane touched an Amoy checkout")
	}
}

func TestReconcile_TokenNotTheChainsUSDC_NotSent(t *testing.T) {
	l := newLaneSetup(t)
	c := l.accepted(t, "co_wrongTokenAAAAAAAAAAA", "sepolia")
	row := l.repo.rows[c.ID]
	row.Token = network(t, "base-sepolia").USDC
	l.repo.rows[c.ID] = row

	l.rec.Step(context.Background())
	if got := l.status(t, c.ID); got != StatusSubmitting || len(l.sepSender.sends) != 0 {
		t.Fatalf("status %s, %d sends; want untouched and unsent", got, len(l.sepSender.sends))
	}
}

func TestNewReconciler_DuplicateLanePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("two lanes for one chain did not panic")
		}
	}()
	sep := network(t, "sepolia")
	NewReconciler(newMemRepo(), []Lane{{Network: sep}, {Network: sep}}, time.Now)
}
