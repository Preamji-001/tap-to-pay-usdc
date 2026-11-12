package store

import (
	"context"
	"errors"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
)

// openTestStore connects to TEST_DATABASE_URL, migrates and empties checkouts.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres tests (docker compose up -d, see .env.example)")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "TRUNCATE checkouts"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func sampleCheckout(id string, nonceByte byte) *checkout.Checkout {
	created := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	var nonce [32]byte
	nonce[0], nonce[31] = nonceByte, 0xAB
	return &checkout.Checkout{
		ID:                id,
		AmountBaseUnits:   big.NewInt(12500000),
		Merchant:          common.HexToAddress("0x3CC845A0014aFcB0D52F673E0a98039074F1474c"),
		Token:             common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"),
		ChainID:           11155111,
		Nonce:             nonce,
		ValidAfter:        0,
		ValidBefore:       created.Add(15 * time.Minute).Unix(),
		MerchantReference: "ORDER-1",
		Status:            checkout.StatusPending,
		CreatedAt:         created,
		ExpiresAt:         created.Add(15 * time.Minute),
		UpdatedAt:         created,
	}
}

func TestMigrateIdempotent(t *testing.T) {
	s := openTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestInsertGetRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	c := sampleCheckout("co_AAAAAAAAAAAAAAAAAAAAAA", 1)
	// Above 2^64 to prove the numeric column keeps full precision.
	c.AmountBaseUnits, _ = new(big.Int).SetString("123456789012345678901234567890", 10)
	payer := common.HexToAddress("0x84DFa4F646D72251d9D40357f233681148A06cD0")
	tx := common.HexToHash("0x" + "11" + "22000000000000000000000000000000000000000000000000000000000033")
	block, relayerNonce := uint64(9_000_000), uint64(7)
	c.Status = checkout.StatusConfirmed
	c.Payer, c.Signature, c.TxHash = &payer, []byte{1, 2, 3}, &tx
	c.FailureReason, c.ConfirmedBlock, c.RelayerNonce = "none", &block, &relayerNonce

	if err := s.Insert(ctx, c); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.AmountBaseUnits.Cmp(c.AmountBaseUnits) != 0 {
		t.Errorf("amount = %s, want %s", got.AmountBaseUnits, c.AmountBaseUnits)
	}
	if got.Merchant != c.Merchant || got.Token != c.Token || got.ChainID != c.ChainID || got.Nonce != c.Nonce {
		t.Errorf("addresses/chain/nonce differ: %+v", got)
	}
	if got.ValidAfter != c.ValidAfter || got.ValidBefore != c.ValidBefore || got.MerchantReference != c.MerchantReference || got.Status != c.Status {
		t.Errorf("window/reference/status differ: %+v", got)
	}
	if got.Payer == nil || *got.Payer != payer || string(got.Signature) != string(c.Signature) || got.TxHash == nil || *got.TxHash != tx {
		t.Errorf("payer/signature/tx differ: %+v", got)
	}
	if got.FailureReason != "none" || got.ConfirmedBlock == nil || *got.ConfirmedBlock != block || got.RelayerNonce == nil || *got.RelayerNonce != relayerNonce {
		t.Errorf("failure/block/relayer nonce differ: %+v", got)
	}
	if !got.CreatedAt.Equal(c.CreatedAt) || !got.ExpiresAt.Equal(c.ExpiresAt) || !got.UpdatedAt.Equal(c.UpdatedAt) {
		t.Errorf("timestamps differ: %v %v %v", got.CreatedAt, got.ExpiresAt, got.UpdatedAt)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", got.CreatedAt.Location())
	}
}

func TestInsertGetNullOptionalFields(t *testing.T) {
	s := openTestStore(t)
	c := sampleCheckout("co_BBBBBBBBBBBBBBBBBBBBBB", 2)
	if err := s.Insert(context.Background(), c); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Get(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Payer != nil || got.Signature != nil || got.TxHash != nil || got.ConfirmedBlock != nil || got.RelayerNonce != nil {
		t.Errorf("optional fields should be nil: %+v", got)
	}
}

func TestGetMissing(t *testing.T) {
	s := openTestStore(t)
	_, err := s.Get(context.Background(), "co_CCCCCCCCCCCCCCCCCCCCCC")
	if !errors.Is(err, checkout.ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
}

func TestDuplicateNonceRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.Insert(ctx, sampleCheckout("co_DDDDDDDDDDDDDDDDDDDDDD", 3)); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	if err := s.Insert(ctx, sampleCheckout("co_EEEEEEEEEEEEEEEEEEEEEE", 3)); err == nil {
		t.Fatal("second Insert with the same nonce succeeded")
	}
}

func TestMarkSubmitting_FirstWinsSecondLoses(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := sampleCheckout("co_markSubmittingAAAAAAA", 1)
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	payer := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	sig := []byte{1, 2, 3}
	at := c.CreatedAt.Add(time.Minute)

	if ok, err := s.MarkSubmitting(ctx, c.ID, sepoliaBinding, payer, sig, at); err != nil || !ok {
		t.Fatalf("first MarkSubmitting = %v, %v; want true", ok, err)
	}
	if ok, err := s.MarkSubmitting(ctx, c.ID, sepoliaBinding, payer, sig, at); err != nil || ok {
		t.Fatalf("second MarkSubmitting = %v, %v; want false", ok, err)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != checkout.StatusSubmitting || got.Payer == nil || *got.Payer != payer ||
		string(got.Signature) != string(sig) || !got.UpdatedAt.Equal(at) {
		t.Fatalf("after MarkSubmitting: %+v", got)
	}
}

func TestMarkSubmitting_Concurrent20_ExactlyOneWins(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := sampleCheckout("co_markSubmittingBBBBBBB", 2)
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	var (
		wg   sync.WaitGroup
		wins atomic.Int32
	)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payer := common.BigToAddress(big.NewInt(int64(i + 1)))
			ok, err := s.MarkSubmitting(ctx, c.ID, sepoliaBinding, payer, []byte{byte(i)}, time.Now())
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d winners, want exactly 1", wins.Load())
	}
}

func TestMarkSubmitting_UnknownID_False(t *testing.T) {
	s := openTestStore(t)
	ok, err := s.MarkSubmitting(context.Background(), "co_doesNotExistCCCCCCCC", sepoliaBinding, common.Address{}, nil, time.Now())
	if err != nil || ok {
		t.Fatalf("MarkSubmitting = %v, %v; want false, nil", ok, err)
	}
}

func TestTransition_ConditionalAndPartial(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := sampleCheckout("co_transitionAAAAAAAAAAA", 3)
	c.CreatedBlock = 9_000_123
	if err := s.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	payer := common.HexToAddress("0xaa")
	if ok, err := s.MarkSubmitting(ctx, c.ID, sepoliaBinding, payer, []byte{1}, c.CreatedAt); !ok || err != nil {
		t.Fatal(ok, err)
	}

	hash, rn := common.HexToHash("0x01"), uint64(5)
	at := c.CreatedAt.Add(time.Minute)
	if ok, err := s.Transition(ctx, c.ID, checkout.StatusPending, checkout.Update{Status: checkout.StatusSubmitted, At: at}); ok || err != nil {
		t.Fatalf("transition from the wrong status = %v, %v", ok, err)
	}
	if ok, err := s.Transition(ctx, c.ID, checkout.StatusSubmitting,
		checkout.Update{Status: checkout.StatusSubmitted, TxHash: &hash, RelayerNonce: &rn, At: at}); !ok || err != nil {
		t.Fatalf("submitting → submitted = %v, %v", ok, err)
	}
	block := uint64(42)
	if ok, err := s.Transition(ctx, c.ID, checkout.StatusSubmitted,
		checkout.Update{Status: checkout.StatusFailed, FailureReason: "transaction reverted", ConfirmedBlock: &block, At: at}); !ok || err != nil {
		t.Fatalf("submitted → failed = %v, %v", ok, err)
	}

	got, err := s.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != checkout.StatusFailed || *got.Payer != payer || *got.TxHash != hash || *got.RelayerNonce != 5 ||
		got.FailureReason != "transaction reverted" || *got.ConfirmedBlock != 42 || got.CreatedBlock != 9_000_123 {
		t.Fatalf("after transitions: %+v", got)
	}
}

func TestListActive_OnlyNonTerminal(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	statuses := []checkout.Status{checkout.StatusPending, checkout.StatusSubmitting, checkout.StatusSubmitted,
		checkout.StatusConfirmed, checkout.StatusFailed, checkout.StatusExpired}
	for i, st := range statuses {
		c := sampleCheckout("co_listActive"+string(rune('A'+i))+"AAAAAAAAAA", byte(10+i))
		c.Status = st
		if err := s.Insert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d active, want 3", len(got))
	}
	for _, c := range got {
		if c.Status.Terminal() {
			t.Errorf("terminal %s listed", c.Status)
		}
	}
}
