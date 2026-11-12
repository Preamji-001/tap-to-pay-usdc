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
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

func (f *fakeChain) BlockNumber(context.Context) (uint64, error) {
	if err := f.record(); err != nil {
		return 0, err
	}
	return f.head, nil
}

func (f *fakeChain) FindAuthorizationUsed(_ context.Context, payer *common.Address, nonce [32]byte, from uint64) (*chain.AuthorizationLog, error) {
	if err := f.record(); err != nil {
		return nil, err
	}
	if f.authLog == nil || f.authLog.Block < from || (payer != nil && *payer != f.authLog.Payer) {
		return nil, nil
	}
	return f.authLog, nil
}

func (f *fakeChain) Receipt(_ context.Context, h common.Hash) (*types.Receipt, error) {
	if err := f.record(); err != nil {
		return nil, err
	}
	return f.receipts[h], nil
}

func (f *fakeChain) TransactionTo(_ context.Context, h common.Hash) (*common.Address, bool, error) {
	if err := f.record(); err != nil {
		return nil, false, err
	}
	to, ok := f.known[h]
	if !ok {
		return nil, false, nil
	}
	return &to, true, nil
}

// fakeSender hands out hashes 0x…01, 0x…02, … and records each send.
type fakeSender struct {
	mu     sync.Mutex
	err    error
	sends  []*uint64 // the reuseNonce of each send
	nonce  uint64
	onSend func(hash common.Hash)
}

func (s *fakeSender) Send(_ context.Context, _ chain.TransferAuthorization, _ []byte, reuse *uint64) (common.Hash, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return common.Hash{}, 0, s.err
	}
	s.sends = append(s.sends, reuse)
	n := s.nonce
	if reuse != nil {
		n = *reuse
	} else {
		s.nonce++
	}
	h := common.BigToHash(big.NewInt(int64(len(s.sends))))
	if s.onSend != nil {
		s.onSend(h)
	}
	return h, n, nil
}

var (
	authorizationUsedTopic = crypto.Keccak256Hash([]byte("AuthorizationUsed(address,bytes32)"))
	transferTopic          = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
)

// receiptFor proves c was paid by payer: status 1 plus both logs from c's token.
func receiptFor(c *Checkout, payer common.Address, block uint64) *types.Receipt {
	return &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		BlockNumber: new(big.Int).SetUint64(block),
		Logs: []*types.Log{
			{Address: c.Token, Topics: []common.Hash{authorizationUsedTopic, common.BytesToHash(payer.Bytes()), common.Hash(c.Nonce)}},
			{
				Address: c.Token,
				Topics:  []common.Hash{transferTopic, common.BytesToHash(payer.Bytes()), common.BytesToHash(c.Merchant.Bytes())},
				Data:    common.LeftPadBytes(c.AmountBaseUnits.Bytes(), 32),
			},
		},
	}
}

type recFixture struct {
	*authFixture
	rec    *Reconciler
	sender *fakeSender
}

// newRecFixture has a checkout already accepted (submitting), at block 1000.
func newRecFixture(t *testing.T, confirmations uint64) *recFixture {
	t.Helper()
	f := &recFixture{authFixture: newAuthFixture(t), sender: &fakeSender{nonce: 40}}
	f.chain.head = 1_000
	f.chain.receipts = map[common.Hash]*types.Receipt{}
	f.chain.known = map[common.Hash]common.Address{}
	f.rec = NewReconciler(f.repo, sepoliaLane(f.chain, f.sender, confirmations), func() time.Time { return *f.now })
	if _, err := f.authorize(f.validSig(t)); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *recFixture) row(t *testing.T) *Checkout {
	t.Helper()
	c, err := f.repo.Get(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *recFixture) step() { f.rec.Step(context.Background()) }

// mine records a successful tx to the token in block, proving f.c's payment.
func (f *recFixture) mine(t *testing.T, h common.Hash, block uint64) {
	t.Helper()
	f.chain.receipts[h] = receiptFor(f.row(t), f.payer, block)
	f.chain.known[h] = testToken
}

func (f *recFixture) want(t *testing.T, status Status) *Checkout {
	t.Helper()
	c := f.row(t)
	if c.Status != status {
		t.Fatalf("status = %s (%q), want %s", c.Status, c.FailureReason, status)
	}
	return c
}

func TestReconcile_SendsThenConfirms(t *testing.T) {
	f := newRecFixture(t, 1)
	f.step()
	c := f.want(t, StatusSubmitted)
	if len(f.sender.sends) != 1 || f.sender.sends[0] != nil || *c.RelayerNonce != 40 || c.TxHash == nil {
		t.Fatalf("after send: %d sends, row %+v", len(f.sender.sends), c)
	}

	f.step() // no receipt yet: waits
	f.want(t, StatusSubmitted)

	f.mine(t, *c.TxHash, 1_000)
	f.step()
	c = f.want(t, StatusConfirmed)
	if *c.ConfirmedBlock != 1_000 || *c.TxHash != common.BigToHash(big.NewInt(1)) {
		t.Fatalf("confirmed row %+v", c)
	}
	if len(f.sender.sends) != 1 {
		t.Fatalf("%d sends, want 1", len(f.sender.sends))
	}
}

func TestReconcile_WaitsForConfirmations(t *testing.T) {
	f := newRecFixture(t, 3)
	f.step()
	f.mine(t, *f.row(t).TxHash, 999)
	f.chain.head = 1_000 // 2 confirmations
	f.step()
	f.want(t, StatusSubmitted)
	f.chain.head = 1_001
	f.step()
	f.want(t, StatusConfirmed)
}

func TestReconcile_RPCErrorsAndDelayedReceipt_StaySubmitted(t *testing.T) {
	f := newRecFixture(t, 1)
	f.step()
	hash := *f.row(t).TxHash
	f.chain.known[hash] = testToken // known to the node, not mined yet

	f.chain.err = errors.New("connection refused")
	f.step()
	f.want(t, StatusSubmitted)
	f.chain.err = nil
	*f.now = f.now.Add(30 * time.Minute) // a long wait is still not a failure
	f.step()
	f.want(t, StatusSubmitted)

	f.mine(t, hash, 1_000)
	f.step()
	f.want(t, StatusConfirmed)
}

func TestReconcile_RevertedReceipt_Failed(t *testing.T) {
	f := newRecFixture(t, 1)
	f.step()
	hash := *f.row(t).TxHash
	f.mine(t, hash, 1_000)
	f.chain.receipts[hash].Status = types.ReceiptStatusFailed
	f.step()
	if c := f.want(t, StatusFailed); c.FailureReason != "transaction reverted" {
		t.Fatalf("reason %q", c.FailureReason)
	}
}

func TestReconcile_WrongLogs_Failed(t *testing.T) {
	for name, change := range map[string]func(r *types.Receipt){
		"wrong amount": func(r *types.Receipt) { r.Logs[1].Data = common.LeftPadBytes([]byte{1}, 32) },
		"wrong recipient": func(r *types.Receipt) {
			r.Logs[1].Topics[2] = common.BytesToHash(common.HexToAddress("0xbad").Bytes())
		},
		"logs from another token":     func(r *types.Receipt) { r.Logs[1].Address = common.HexToAddress("0xdead") },
		"no AuthorizationUsed":        func(r *types.Receipt) { r.Logs = r.Logs[1:] },
		"AuthorizationUsed elsewhere": func(r *types.Receipt) { r.Logs[0].Address = common.HexToAddress("0xdead") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRecFixture(t, 1)
			f.step()
			hash := *f.row(t).TxHash
			f.mine(t, hash, 1_000)
			change(f.chain.receipts[hash])
			f.step()
			if c := f.want(t, StatusFailed); c.FailureReason == "" {
				t.Fatal("no failure reason")
			}
		})
	}
}

func TestReconcile_TxNotToToken_Failed(t *testing.T) {
	f := newRecFixture(t, 1)
	f.step()
	hash := *f.row(t).TxHash
	f.mine(t, hash, 1_000)
	f.chain.known[hash] = common.HexToAddress("0xdead")
	f.step()
	f.want(t, StatusFailed)
}

func TestReconcile_FrontRunAfterSend_ConfirmedWithTheirHash(t *testing.T) {
	f := newRecFixture(t, 1)
	f.step()
	ours := *f.row(t).TxHash
	theirs := common.HexToHash("0xbeef")

	// Their tx used the authorization; ours then reverted.
	f.chain.used = true
	f.chain.authLog = &chain.AuthorizationLog{Payer: f.payer, TxHash: theirs, Block: 1_000}
	f.chain.receipts[theirs] = receiptFor(f.row(t), f.payer, 1_000)
	f.mine(t, ours, 1_000)
	f.chain.receipts[ours].Status = types.ReceiptStatusFailed

	f.step()
	if c := f.want(t, StatusConfirmed); *c.TxHash != theirs {
		t.Fatalf("tx hash %s, want theirs %s", c.TxHash, theirs)
	}
}

func TestReconcile_FrontRunBeforeSend_NoSendThenConfirmed(t *testing.T) {
	f := newRecFixture(t, 1)
	theirs := common.HexToHash("0xbeef")
	f.sender.err = &chain.RevertError{Reason: "FiatTokenV2: authorization is used or canceled"}
	f.chain.used = true

	// The log isn't indexed yet: wait rather than fail.
	f.step()
	f.want(t, StatusSubmitting)

	f.chain.authLog = &chain.AuthorizationLog{Payer: f.payer, TxHash: theirs, Block: 1_000}
	f.chain.receipts[theirs] = receiptFor(f.row(t), f.payer, 1_000)
	f.step()
	if c := f.want(t, StatusConfirmed); *c.TxHash != theirs {
		t.Fatalf("tx hash %s", c.TxHash)
	}
	if len(f.sender.sends) != 0 {
		t.Fatal("sent although the authorization was used")
	}
}

func TestReconcile_SendRevert_Failed(t *testing.T) {
	f := newRecFixture(t, 1)
	f.sender.err = &chain.RevertError{Reason: "ERC20: transfer amount exceeds balance"}
	f.step()
	if c := f.want(t, StatusFailed); c.FailureReason != "transfer would revert: ERC20: transfer amount exceeds balance" {
		t.Fatalf("reason %q", c.FailureReason)
	}
}

func TestReconcile_SendRPCError_Retried(t *testing.T) {
	f := newRecFixture(t, 1)
	f.sender.err = errors.New("connection refused")
	f.step()
	f.want(t, StatusSubmitting)
	f.sender.err = nil
	f.step()
	f.want(t, StatusSubmitted)
}

func TestReconcile_CrashRecovery_FromSubmitting(t *testing.T) {
	// The tx was sent and mined, but we crashed before storing its hash.
	f := newRecFixture(t, 1)
	sent := common.HexToHash("0x5e47")
	f.chain.used = true
	f.chain.authLog = &chain.AuthorizationLog{Payer: f.payer, TxHash: sent, Block: 1_000}
	f.chain.receipts[sent] = receiptFor(f.row(t), f.payer, 1_000)
	f.step()
	if c := f.want(t, StatusConfirmed); *c.TxHash != sent {
		t.Fatalf("tx hash %s", c.TxHash)
	}
	if len(f.sender.sends) != 0 {
		t.Fatal("resent an authorization that was already used")
	}
}

func TestReconcile_CrashRecovery_NeverSent_Sends(t *testing.T) {
	f := newRecFixture(t, 1) // submitting, unused: as after a crash before sending
	f.step()
	f.want(t, StatusSubmitted)
}

func TestReconcile_SubmittingTooLate_Failed(t *testing.T) {
	f := newRecFixture(t, 1)
	*f.now = time.Unix(f.c.ValidBefore, 0).Add(-sendMargin)
	f.step()
	f.want(t, StatusFailed)
	if len(f.sender.sends) != 0 {
		t.Fatal("sent too close to validBefore")
	}
}

func TestReconcile_DroppedTx_ResentAtSameNonce(t *testing.T) {
	f := newRecFixture(t, 1)
	f.step()
	first := f.row(t)

	*f.now = f.now.Add(dropAfter - time.Second)
	f.step()
	f.want(t, StatusSubmitted)
	if len(f.sender.sends) != 1 {
		t.Fatal("resent before dropAfter")
	}

	*f.now = f.now.Add(2 * time.Second)
	f.step()
	c := f.want(t, StatusSubmitted)
	if len(f.sender.sends) != 2 || f.sender.sends[1] == nil || *f.sender.sends[1] != *first.RelayerNonce {
		t.Fatalf("sends %v, want a resend reusing nonce %d", f.sender.sends, *first.RelayerNonce)
	}
	if *c.TxHash == *first.TxHash || *c.RelayerNonce != *first.RelayerNonce {
		t.Fatalf("after resend: %+v", c)
	}
}

func TestReconcile_StuckButKnown_NotResent(t *testing.T) {
	f := newRecFixture(t, 1)
	f.step()
	f.chain.known[*f.row(t).TxHash] = testToken
	*f.now = f.now.Add(time.Hour)
	f.step()
	f.want(t, StatusSubmitted)
	if len(f.sender.sends) != 1 {
		t.Fatal("resent a tx the node still knows")
	}
}

func TestReconcile_PendingPaidByOutsider_Confirmed(t *testing.T) {
	f := newRecFixture(t, 1)
	// A second checkout nobody authorized with us, but whose signature was used elsewhere.
	c, err := f.svc.Create(context.Background(), CreateRequest{Amount: "3"})
	if err != nil {
		t.Fatal(err)
	}
	outsider, theirs := common.HexToAddress("0xcafe"), common.HexToHash("0xbeef")
	f.chain.authLog = &chain.AuthorizationLog{Payer: outsider, TxHash: theirs, Block: 1_000}
	paid := *c
	paid.Token = testToken // the logs come from Sepolia USDC
	f.chain.receipts[theirs] = receiptFor(&paid, outsider, 1_000)

	f.step()
	if got, _ := f.repo.Get(context.Background(), c.ID); got.Status != StatusPending {
		t.Fatalf("touched before expiry: %s", got.Status)
	}
	*f.now = c.ExpiresAt
	f.step()
	got, _ := f.repo.Get(context.Background(), c.ID)
	if got.Status != StatusConfirmed || *got.Payer != outsider || *got.TxHash != theirs {
		t.Fatalf("got %+v", got)
	}
	// Nobody authorized it with us, so finding the payment is what binds its chain.
	if got.ChainID != 11155111 || got.Token != testToken {
		t.Fatalf("bound to chain %d token %s, want Sepolia", got.ChainID, got.Token.Hex())
	}
}

func TestReconcile_PendingExpired(t *testing.T) {
	f := newAuthFixture(t)
	f.chain.head = 1_000
	rec := NewReconciler(f.repo, sepoliaLane(f.chain, &fakeSender{}, 1), func() time.Time { return *f.now })
	*f.now = f.c.ExpiresAt
	rec.Step(context.Background())
	if f.status(t) != StatusExpired {
		t.Fatalf("status %s", f.status(t))
	}
}

func TestReconcile_PendingExpired_LogDoesNotVerify_Expired(t *testing.T) {
	f := newAuthFixture(t)
	f.chain.head = 1_000
	theirs := common.HexToHash("0xbeef")
	f.chain.authLog = &chain.AuthorizationLog{Payer: f.payer, TxHash: theirs, Block: 1_000}
	r := receiptFor(f.c, f.payer, 1_000)
	r.Logs = r.Logs[:1] // no Transfer
	f.chain.receipts = map[common.Hash]*types.Receipt{theirs: r}
	rec := NewReconciler(f.repo, sepoliaLane(f.chain, &fakeSender{}, 1), func() time.Time { return *f.now })
	*f.now = f.c.ExpiresAt
	rec.Step(context.Background())
	if f.status(t) != StatusExpired {
		t.Fatalf("status %s", f.status(t))
	}
}

func TestReconcile_Concurrent20Authorizations_OneSend(t *testing.T) {
	f := newAuthFixture(t)
	f.chain.head = 1_000
	sender := &fakeSender{}
	rec := NewReconciler(f.repo, sepoliaLane(f.chain, sender, 1), func() time.Time { return *f.now })
	p := testParams()
	f.svc = NewService(f.repo, p, func() time.Time { return *f.now }, allChains(p.Networks, f.chain), rec)

	sig := f.validSig(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.authorize(sig)
		}()
	}
	wg.Wait()
	rec.Step(context.Background())
	rec.Step(context.Background())
	if len(sender.sends) != 1 {
		t.Fatalf("%d sends, want 1", len(sender.sends))
	}
}

func TestReconciler_SubmitNeverBlocks(t *testing.T) {
	rec := NewReconciler(newMemRepo(), sepoliaLane(&fakeChain{}, &fakeSender{}, 1), time.Now)
	for range 3 {
		rec.Submit(context.Background(), nil)
	}
}

func TestCreate_RecordsBlock_AndNeedsChain(t *testing.T) {
	f := newAuthFixture(t)
	if b, ok := f.c.StartBlocks[11155111]; !ok || b != 0 || f.c.CreatedBlock != 0 {
		t.Fatalf("StartBlocks = %v, CreatedBlock %d; want Sepolia at the fake head 0, nothing bound", f.c.StartBlocks, f.c.CreatedBlock)
	}
	f.chain.head = 4_242
	c, err := f.svc.Create(context.Background(), CreateRequest{Amount: "1"})
	if err != nil || c.StartBlocks[11155111] != 4_242 {
		t.Fatalf("StartBlocks = %v (%v), want Sepolia at 4242", c.StartBlocks, err)
	}
	f.chain.err = errors.New("down")
	if _, err := f.svc.Create(context.Background(), CreateRequest{Amount: "1"}); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("err = %v, want ErrChainUnavailable", err)
	}
}

func TestReconcile_MalformedRows_FailedNotPanic(t *testing.T) {
	repo := newMemRepo()
	for id, c := range map[string]Checkout{
		"co_submittingNoPayerAAAA": {Status: StatusSubmitting},
		"co_submittedNoHashAAAAAA": {Status: StatusSubmitted, Payer: &testMerchant, Signature: []byte{1}},
	} {
		c.ID, c.AmountBaseUnits, c.ExpiresAt = id, big.NewInt(1), testNow.Add(time.Hour)
		c.ChainID, c.Token = 11155111, testToken
		repo.rows[id] = c
	}
	NewReconciler(repo, sepoliaLane(&fakeChain{}, &fakeSender{}, 1), func() time.Time { return testNow }).Step(context.Background())
	for id, c := range repo.rows {
		if c.Status != StatusFailed || c.FailureReason == "" {
			t.Errorf("%s: %s %q, want failed with a reason", id, c.Status, c.FailureReason)
		}
	}
}
