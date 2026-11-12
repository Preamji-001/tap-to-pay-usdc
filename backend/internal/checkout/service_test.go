package checkout

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

// memRepo is an in-memory Repository for tests.
type memRepo struct {
	mu   sync.Mutex
	rows map[string]Checkout
}

func newMemRepo() *memRepo { return &memRepo{rows: map[string]Checkout{}} }

func (r *memRepo) Insert(_ context.Context, c *Checkout) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[c.ID] = *c
	return nil
}

func (r *memRepo) Get(_ context.Context, id string) (*Checkout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.rows[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &c, nil
}

func (r *memRepo) MarkSubmitting(_ context.Context, id string, b Binding, payer common.Address, sig []byte, at time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.rows[id]
	if !ok || c.Status != StatusPending || (c.Bound() && (c.ChainID != b.ChainID || c.Token != b.Token)) {
		return false, nil
	}
	if !c.Bound() {
		c.ChainID, c.Token, c.CreatedBlock = b.ChainID, b.Token, b.CreatedBlock
	}
	c.Status, c.Payer, c.Signature, c.UpdatedAt = StatusSubmitting, &payer, sig, at
	r.rows[id] = c
	return true, nil
}

func (r *memRepo) Transition(_ context.Context, id string, from Status, u Update) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.rows[id]
	if !ok || c.Status != from || (u.Bind != nil && c.Bound() && c.ChainID != u.Bind.ChainID) {
		return false, nil
	}
	if u.Bind != nil && !c.Bound() {
		c.ChainID, c.Token, c.CreatedBlock = u.Bind.ChainID, u.Bind.Token, u.Bind.CreatedBlock
	}
	c.Status, c.UpdatedAt = u.Status, u.At
	if u.Payer != nil {
		c.Payer = u.Payer
	}
	if u.TxHash != nil {
		c.TxHash = u.TxHash
	}
	if u.FailureReason != "" {
		c.FailureReason = u.FailureReason
	}
	if u.ConfirmedBlock != nil {
		c.ConfirmedBlock = u.ConfirmedBlock
	}
	if u.RelayerNonce != nil {
		c.RelayerNonce = u.RelayerNonce
	}
	r.rows[id] = c
	return true, nil
}

func (r *memRepo) ListActive(context.Context) ([]*Checkout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Checkout
	for _, c := range r.rows {
		if !c.Status.Terminal() {
			out = append(out, &c)
		}
	}
	return out, nil
}

var (
	testNow      = time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	testMerchant = common.HexToAddress("0x3CC845A0014aFcB0D52F673E0a98039074F1474c")
	testToken    = common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
)

// testRegistry enables the named networks (first = primary) with dummy RPC URLs.
func testRegistry(keys ...string) *chain.Registry {
	var nets []chain.Network
	for _, k := range keys {
		n, ok := chain.KnownNetwork(k)
		if !ok {
			panic("unknown network " + k)
		}
		n.RPCURL = "https://" + k + ".invalid"
		nets = append(nets, n)
	}
	r, err := chain.NewRegistry(nets)
	if err != nil {
		panic(err)
	}
	return r
}

func testParams() Params {
	return Params{
		Merchant:        testMerchant,
		Networks:        testRegistry("sepolia"),
		TTL:             15 * time.Minute,
		MaxAmount:       big.NewInt(1_000_000_000),
		FrontendBaseURL: "http://localhost:5173",
	}
}

func newTestService(repo Repository, now *time.Time) *Service {
	p := testParams()
	return NewService(repo, p, func() time.Time { return *now }, allChains(p.Networks, &fakeChain{}), NoopRelayer{})
}

// allChains wires every network in reg to the same fake reads.
func allChains(reg *chain.Registry, c Chain) Chains {
	out := Chains{}
	for _, n := range reg.Networks() {
		out[n.ChainID] = c
	}
	return out
}

// sepoliaLane is the reconciler's single lane in most tests.
func sepoliaLane(c Chain, s Sender, confirmations uint64) []Lane {
	n, _ := chain.KnownNetwork("sepolia")
	n.Confirmations = confirmations
	return []Lane{{Network: n, Chain: c, Sender: s}}
}

func TestCreateSetsServerFields(t *testing.T) {
	repo := newMemRepo()
	now := testNow
	svc := newTestService(repo, &now)

	c, err := svc.Create(context.Background(), CreateRequest{Amount: "12.50", MerchantReference: "ORDER-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.AmountBaseUnits.Cmp(big.NewInt(12500000)) != 0 {
		t.Errorf("AmountBaseUnits = %s", c.AmountBaseUnits)
	}
	// The chain is chosen when the customer pays (L4), so none is set yet.
	if c.Merchant != testMerchant || c.Bound() || c.Token != (common.Address{}) {
		t.Errorf("server fields = %s %s %d", c.Merchant, c.Token, c.ChainID)
	}
	if c.Status != StatusPending {
		t.Errorf("Status = %q", c.Status)
	}
	if !c.ExpiresAt.Equal(testNow.Add(15 * time.Minute)) {
		t.Errorf("ExpiresAt = %v", c.ExpiresAt)
	}
	if c.ValidAfter != 0 || c.ValidBefore != c.ExpiresAt.Unix() {
		t.Errorf("validity window = %d..%d", c.ValidAfter, c.ValidBefore)
	}
	if !ValidID(c.ID) {
		t.Errorf("ID %q is not valid", c.ID)
	}
	if c.Nonce == ([32]byte{}) {
		t.Error("nonce is zero")
	}
	if c.MerchantReference != "ORDER-1" {
		t.Errorf("MerchantReference = %q", c.MerchantReference)
	}
	if _, err := repo.Get(context.Background(), c.ID); err != nil {
		t.Errorf("not stored: %v", err)
	}
}

func TestCreateUniqueIDsAndNonces(t *testing.T) {
	now := testNow
	svc := newTestService(newMemRepo(), &now)
	ids := map[string]bool{}
	nonces := map[[32]byte]bool{}
	for i := 0; i < 1000; i++ {
		c, err := svc.Create(context.Background(), CreateRequest{Amount: "1"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if ids[c.ID] || nonces[c.Nonce] {
			t.Fatalf("duplicate id or nonce at %d", i)
		}
		ids[c.ID], nonces[c.Nonce] = true, true
	}
}

func TestCreateRejects(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateRequest
		wantErr error
	}{
		{"zero amount", CreateRequest{Amount: "0"}, ErrInvalidAmount},
		{"over max", CreateRequest{Amount: "1000.000001"}, ErrAmountTooLarge},
		{"max is inclusive", CreateRequest{Amount: "1000"}, nil},
		{"65 char reference", CreateRequest{Amount: "1", MerchantReference: strings.Repeat("a", 65)}, ErrInvalidReference},
		{"control char reference", CreateRequest{Amount: "1", MerchantReference: "ORDER\n1"}, ErrInvalidReference},
		{"empty reference", CreateRequest{Amount: "1"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := testNow
			_, err := newTestService(newMemRepo(), &now).Create(context.Background(), tt.req)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetExpiry(t *testing.T) {
	repo := newMemRepo()
	now := testNow
	svc := newTestService(repo, &now)
	c, err := svc.Create(context.Background(), CreateRequest{Amount: "1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	now = c.ExpiresAt.Add(-time.Nanosecond)
	if got, _ := svc.Get(context.Background(), c.ID); got.Status != StatusPending {
		t.Errorf("1ns before expiry: Status = %q, want pending", got.Status)
	}
	now = c.ExpiresAt
	if got, _ := svc.Get(context.Background(), c.ID); got.Status != StatusExpired {
		t.Errorf("at expiry: Status = %q, want expired", got.Status)
	}

	confirmed := *c
	confirmed.Status = StatusConfirmed
	_ = repo.Insert(context.Background(), &confirmed)
	now = c.ExpiresAt.Add(time.Hour)
	if got, _ := svc.Get(context.Background(), c.ID); got.Status != StatusConfirmed {
		t.Errorf("confirmed past expiry: Status = %q, want confirmed", got.Status)
	}
}

func TestGetNotFound(t *testing.T) {
	now := testNow
	svc := newTestService(newMemRepo(), &now)
	if _, err := svc.Get(context.Background(), "co_AAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v, want ErrNotFound", err)
	}
	for _, id := range []string{"", "../x", strings.Repeat("a", 500), "co_" + strings.Repeat("A", 21)} {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
		if _, err := svc.Get(context.Background(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestCheckoutURL(t *testing.T) {
	now := testNow
	for _, base := range []string{"http://localhost:5173", "http://localhost:5173/"} {
		p := testParams()
		p.FrontendBaseURL = base
		svc := NewService(newMemRepo(), p, func() time.Time { return now }, allChains(p.Networks, &fakeChain{}), NoopRelayer{})
		if got := svc.CheckoutURL("co_x"); got != "http://localhost:5173/checkout/co_x" {
			t.Errorf("base %q: CheckoutURL = %q", base, got)
		}
	}
}

func TestCheckout_Authorization_UsesStoredFields(t *testing.T) {
	now := testNow
	svc := newTestService(newMemRepo(), &now)
	c, err := svc.Create(context.Background(), CreateRequest{Amount: "12.50"})
	if err != nil {
		t.Fatal(err)
	}
	from := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	a := c.Authorization(from)
	if a.From != from || a.To != c.Merchant || a.Value.Cmp(c.AmountBaseUnits) != 0 ||
		a.ValidAfter.Int64() != c.ValidAfter || a.ValidBefore.Int64() != c.ValidBefore || a.Nonce != c.Nonce {
		t.Fatalf("Authorization = %+v, checkout = %+v", a, c)
	}
	a.Value.SetInt64(1)
	if c.AmountBaseUnits.Int64() == 1 {
		t.Fatal("Authorization shares the checkout's amount")
	}
}
