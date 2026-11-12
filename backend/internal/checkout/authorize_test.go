package checkout

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

// fakeChain is a Chain with a funded payer and a passing simulation unless
// told otherwise. It counts calls.
type fakeChain struct {
	mu      sync.Mutex
	used    bool
	balance *big.Int // nil means plenty
	simErr  error
	err     error // returned by every call while set
	calls   int
	gate    chan struct{} // while set, every call waits until it is closed

	// What the reconciler reads (reconciler_test.go).
	head     uint64
	authLog  *chain.AuthorizationLog
	receipts map[common.Hash]*types.Receipt
	known    map[common.Hash]common.Address // tx hash → its to
}

func (f *fakeChain) record() error {
	f.mu.Lock()
	f.calls++
	gate, err := f.gate, f.err
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return err
}

func (f *fakeChain) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeChain) AuthorizationUsed(context.Context, common.Address, [32]byte) (bool, error) {
	if err := f.record(); err != nil {
		return false, err
	}
	return f.used, nil
}

func (f *fakeChain) BalanceOf(context.Context, common.Address) (*big.Int, error) {
	if err := f.record(); err != nil {
		return nil, err
	}
	if f.balance == nil {
		return big.NewInt(1_000_000_000_000), nil
	}
	return f.balance, nil
}

func (f *fakeChain) SimulateTransfer(context.Context, chain.TransferAuthorization, []byte) error {
	if err := f.record(); err != nil {
		return err
	}
	return f.simErr
}

type recordingRelayer struct{ submitted atomic.Int32 }

func (r *recordingRelayer) Submit(context.Context, *Checkout) { r.submitted.Add(1) }

type authFixture struct {
	svc     *Service
	repo    *memRepo
	chain   *fakeChain
	relayer *recordingRelayer
	now     *time.Time
	key     *ecdsa.PrivateKey
	payer   common.Address
	c       *Checkout
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	return newAuthFixtureWith(t, testParams())
}

func newAuthFixtureWith(t *testing.T, p Params) *authFixture {
	t.Helper()
	now := testNow
	f := &authFixture{repo: newMemRepo(), chain: &fakeChain{}, relayer: &recordingRelayer{}, now: &now}
	f.svc = NewService(f.repo, p, func() time.Time { return *f.now }, allChains(p.Networks, f.chain), f.relayer)
	var err error
	if f.key, err = crypto.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	f.payer = crypto.PubkeyToAddress(f.key.PublicKey)
	if f.c, err = f.svc.Create(context.Background(), CreateRequest{Amount: "12.50"}); err != nil {
		t.Fatal(err)
	}
	f.chain.calls = 0 // count only what the test itself triggers
	return f
}

func (f *authFixture) signAuth(t *testing.T, a chain.TransferAuthorization, domain common.Hash) []byte {
	t.Helper()
	sig, err := crypto.Sign(a.Digest(domain).Bytes(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return sig
}

// validSig signs exactly what the server expects for the fixture's checkout.
func (f *authFixture) validSig(t *testing.T) []byte {
	return f.signAuth(t, f.c.Authorization(f.payer), f.domain(t))
}

// domain is the separator the server uses for the fixture's checkout.
func (f *authFixture) domain(t *testing.T) common.Hash {
	t.Helper()
	d, err := f.svc.DomainSeparator(f.c, 0)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// authorizeOn submits sig as a payment on chainID.
func (f *authFixture) authorizeOn(chainID int64, sig []byte) (*Checkout, error) {
	return f.svc.Authorize(context.Background(), f.c.ID, chainID, f.payer, sig)
}

func (f *authFixture) row(t *testing.T) *Checkout {
	t.Helper()
	c, err := f.repo.Get(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *authFixture) authorize(sig []byte) (*Checkout, error) {
	return f.svc.Authorize(context.Background(), f.c.ID, 0, f.payer, sig)
}

func (f *authFixture) status(t *testing.T) Status {
	t.Helper()
	c, err := f.repo.Get(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c.Status
}

func TestAuthorize_ValidSignature_Submitting(t *testing.T) {
	f := newAuthFixture(t)
	sig := f.validSig(t)
	c, err := f.authorize(sig)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if c.Status != StatusSubmitting || c.Payer == nil || *c.Payer != f.payer || string(c.Signature) != string(sig) {
		t.Fatalf("checkout = %+v", c)
	}
	if n := f.relayer.submitted.Load(); n != 1 {
		t.Fatalf("relayer called %d times, want 1", n)
	}
}

func TestAuthorize_InvalidSignatures(t *testing.T) {
	f := newAuthFixture(t)
	domain := f.domain(t)
	base := f.c.Authorization(f.payer)
	other, _ := crypto.GenerateKey()

	tamper := func(change func(*chain.TransferAuthorization)) []byte {
		a := base
		change(&a)
		return f.signAuth(t, a, domain)
	}
	valid := f.validSig(t)
	highS := append([]byte(nil), valid...)
	s := new(big.Int).Sub(crypto.S256().Params().N, new(big.Int).SetBytes(valid[32:64]))
	copy(highS[32:64], common.LeftPadBytes(s.Bytes(), 32))
	highS[64] = 27 + 28 - valid[64]
	otherSig, _ := crypto.Sign(base.Digest(domain).Bytes(), other)
	otherSig[64] += 27

	for name, sig := range map[string][]byte{
		"different amount":   tamper(func(a *chain.TransferAuthorization) { a.Value = big.NewInt(1) }),
		"other merchant":     tamper(func(a *chain.TransferAuthorization) { a.To = common.HexToAddress("0xbad") }),
		"later validBefore":  tamper(func(a *chain.TransferAuthorization) { a.ValidBefore = big.NewInt(a.ValidBefore.Int64() + 1) }),
		"other nonce":        tamper(func(a *chain.TransferAuthorization) { a.Nonce[0] ^= 1 }),
		"mainnet domain":     f.signAuth(t, base, chain.DomainSeparator("USDC", "2", 1, testToken)),
		"other token domain": f.signAuth(t, base, chain.DomainSeparator("USDC", "2", 11155111, common.HexToAddress("0x1234"))),
		"wrong signer":       otherSig,
		"high s":             highS,
		"64 bytes":           valid[:64],
		"v = 1":              append(append([]byte(nil), valid[:64]...), 1),
	} {
		if _, err := f.authorize(sig); !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("%s: err = %v, want ErrInvalidSignature", name, err)
		}
	}
	if f.chain.calls != 0 || f.status(t) != StatusPending || f.relayer.submitted.Load() != 0 {
		t.Fatalf("after invalid signatures: %d chain calls, status %s", f.chain.calls, f.status(t))
	}
}

func TestAuthorize_Expired(t *testing.T) {
	f := newAuthFixture(t)
	*f.now = f.c.ExpiresAt.Add(time.Second)
	if _, err := f.authorize(f.validSig(t)); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestAuthorize_AtSafetyMarginBoundary(t *testing.T) {
	f := newAuthFixture(t)
	*f.now = f.c.ExpiresAt.Add(-60 * time.Second)
	if _, err := f.authorize(f.validSig(t)); !errors.Is(err, ErrExpired) {
		t.Fatalf("at expires_at-60s: err = %v, want ErrExpired", err)
	}
	if f.chain.calls != 0 {
		t.Fatalf("expired request made %d chain calls", f.chain.calls)
	}

	*f.now = f.c.ExpiresAt.Add(-61 * time.Second)
	if _, err := f.authorize(f.validSig(t)); err != nil {
		t.Fatalf("at expires_at-61s: %v", err)
	}
}

func TestAuthorize_NonceAlreadyUsed_SubmittingWithoutRelay(t *testing.T) {
	f := newAuthFixture(t)
	f.chain.used = true
	c, err := f.authorize(f.validSig(t))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if c.Status != StatusSubmitting || f.relayer.submitted.Load() != 0 {
		t.Fatalf("status %s, relayer calls %d; want submitting, 0", c.Status, f.relayer.submitted.Load())
	}
	if f.chain.calls != 1 {
		t.Fatalf("%d chain calls, want only authorizationState", f.chain.calls)
	}
}

func TestAuthorize_InsufficientBalance_StaysPending(t *testing.T) {
	f := newAuthFixture(t)
	f.chain.balance = new(big.Int).Sub(f.c.AmountBaseUnits, big.NewInt(1))
	if _, err := f.authorize(f.validSig(t)); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("err = %v, want ErrInsufficientBalance", err)
	}
	if f.status(t) != StatusPending {
		t.Fatalf("status = %s", f.status(t))
	}
}

func TestAuthorize_SimulationRevert_StaysPending(t *testing.T) {
	f := newAuthFixture(t)
	f.chain.simErr = &chain.RevertError{Reason: "FiatTokenV2: invalid signature"}
	_, err := f.authorize(f.validSig(t))
	var re *chain.RevertError
	if !errors.Is(err, ErrSimulationFailed) || !errors.As(err, &re) {
		t.Fatalf("err = %v, want ErrSimulationFailed wrapping the revert", err)
	}
	if f.status(t) != StatusPending {
		t.Fatalf("status = %s", f.status(t))
	}
}

func TestAuthorize_ChainErrorStaysPending(t *testing.T) {
	f := newAuthFixture(t)
	f.chain.err = errors.New("connection refused")
	if _, err := f.authorize(f.validSig(t)); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("err = %v, want ErrChainUnavailable", err)
	}
	if f.status(t) != StatusPending {
		t.Fatalf("status = %s", f.status(t))
	}

	f.chain.err = nil
	if _, err := f.authorize(f.validSig(t)); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
}

func TestAuthorize_SimulationRPCError_Unavailable(t *testing.T) {
	f := newAuthFixture(t)
	f.chain.simErr = errors.New("timeout")
	if _, err := f.authorize(f.validSig(t)); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("err = %v, want ErrChainUnavailable", err)
	}
}

func TestAuthorize_SequentialDuplicate(t *testing.T) {
	f := newAuthFixture(t)
	sig := f.validSig(t)
	if _, err := f.authorize(sig); err != nil {
		t.Fatal(err)
	}
	if _, err := f.authorize(sig); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second: err = %v, want ErrNotPending", err)
	}
	if n := f.relayer.submitted.Load(); n != 1 {
		t.Fatalf("relayer called %d times, want 1", n)
	}
}

func TestAuthorize_Concurrent20_OneRelaySend(t *testing.T) {
	f := newAuthFixture(t)
	sig := f.validSig(t)
	var (
		wg   sync.WaitGroup
		wins atomic.Int32
	)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.authorize(sig)
			switch {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, ErrNotPending):
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || f.relayer.submitted.Load() != 1 {
		t.Fatalf("%d winners, %d relay sends; want 1, 1", wins.Load(), f.relayer.submitted.Load())
	}
}

func TestAuthorize_UnknownID_NotFound(t *testing.T) {
	f := newAuthFixture(t)
	for _, id := range []string{"co_AAAAAAAAAAAAAAAAAAAAAA", "nope"} {
		if _, err := f.svc.Authorize(context.Background(), id, 0, f.payer, f.validSig(t)); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", id, err)
		}
	}
}

func network(t *testing.T, key string) chain.Network {
	t.Helper()
	n, ok := chain.KnownNetwork(key)
	if !ok {
		t.Fatalf("unknown network %s", key)
	}
	return n
}

// L2 + L4: a signature is bound to one chain. The same payer, amount,
// recipient, nonce and window signed under one chain's domain but submitted
// as another chain must be rejected, before any RPC call, leaving the
// checkout pending and unbound.
func TestAuthorize_CrossChainReplayRejected(t *testing.T) {
	tests := []struct{ submittedAs, signedFor string }{
		{"sepolia", "base-sepolia"},
		{"base-sepolia", "sepolia"},
		{"sepolia", "amoy"},
	}
	for _, tt := range tests {
		t.Run(tt.signedFor+" signature submitted as "+tt.submittedAs, func(t *testing.T) {
			p := testParams()
			p.Networks = testRegistry(tt.submittedAs, tt.signedFor)
			f := newAuthFixtureWith(t, p)
			as := network(t, tt.submittedAs).ChainID

			wrong := f.signAuth(t, f.c.Authorization(f.payer), network(t, tt.signedFor).DomainSeparator())
			if _, err := f.authorizeOn(as, wrong); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("Authorize = %v, want ErrInvalidSignature", err)
			}
			if f.chain.calls != 0 || f.status(t) != StatusPending || f.relayer.submitted.Load() != 0 || f.row(t).Bound() {
				t.Fatalf("rejected signature reached the chain (%d calls), status %s, bound %v", f.chain.calls, f.status(t), f.row(t).Bound())
			}

			// The same checkout still accepts a signature for the chain it is submitted as.
			right := f.signAuth(t, f.c.Authorization(f.payer), network(t, tt.submittedAs).DomainSeparator())
			if _, err := f.authorizeOn(as, right); err != nil {
				t.Fatalf("Authorize with the right chain: %v", err)
			}
		})
	}
}

// Unbound, the default domain is the primary network's; any enabled chain
// can be asked for by ID.
func TestDomain_DefaultIsPrimary_AnyEnabledChainOnRequest(t *testing.T) {
	p := testParams()
	p.Networks = testRegistry("base-sepolia", "sepolia")
	f := newAuthFixtureWith(t, p)
	base, sep := network(t, "base-sepolia"), network(t, "sepolia")
	if d, err := f.svc.Domain(f.c, 0); err != nil || d != base.TokenParams() {
		t.Fatalf("default Domain = %+v, %v; want Base Sepolia's", d, err)
	}
	if d, err := f.svc.Domain(f.c, sep.ChainID); err != nil || d != sep.TokenParams() {
		t.Fatalf("Domain(sepolia) = %+v, %v", d, err)
	}
	if _, err := f.svc.Domain(f.c, 80002); !errors.Is(err, ErrUnknownChain) {
		t.Fatalf("Domain(amoy, not enabled) = %v, want ErrUnknownChain", err)
	}
}

func TestAuthorize_UnsupportedChain(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Checkout)
	}{
		{"bound chain no longer enabled", func(c *Checkout) { c.ChainID = 80002; c.Token = network(t, "amoy").USDC }},
		{"token is not the bound chain's usdc", func(c *Checkout) { c.ChainID = 11155111; c.Token = network(t, "base-sepolia").USDC }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAuthFixture(t)
			sig := f.validSig(t)
			row := f.repo.rows[f.c.ID]
			tt.mutate(&row)
			f.repo.rows[f.c.ID] = row

			if _, err := f.authorize(sig); !errors.Is(err, ErrUnsupportedChain) {
				t.Fatalf("Authorize = %v, want ErrUnsupportedChain", err)
			}
			if f.chain.calls != 0 || f.status(t) != StatusPending {
				t.Fatalf("chain calls %d, status %s", f.chain.calls, f.status(t))
			}
		})
	}
}

// The domain comes from the checkout row, not from the primary network: a
// Base Sepolia checkout on a Sepolia-primary server verifies Base signatures
// only. (L4 will create such rows; here the row is edited directly.)
func TestAuthorize_DomainFromRowNotPrimary(t *testing.T) {
	p := testParams()
	p.Networks = testRegistry("sepolia", "base-sepolia")
	f := newAuthFixtureWith(t, p)
	base := network(t, "base-sepolia")
	row := f.repo.rows[f.c.ID]
	row.ChainID, row.Token = base.ChainID, base.USDC
	f.repo.rows[f.c.ID] = row
	f.c = &row

	sepoliaSig := f.signAuth(t, f.c.Authorization(f.payer), network(t, "sepolia").DomainSeparator())
	if _, err := f.authorize(sepoliaSig); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("Sepolia-domain signature on a Base checkout: %v, want ErrInvalidSignature", err)
	}
	baseSig := f.signAuth(t, f.c.Authorization(f.payer), base.DomainSeparator())
	if _, err := f.authorize(baseSig); err != nil {
		t.Fatalf("Base-domain signature on a Base checkout: %v", err)
	}
}
