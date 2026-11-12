package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
)

const (
	testAPIKey = "test-api-key-0123456789abcdef0123456789"
	testOrigin = "http://localhost:5173"
)

type memRepo struct {
	mu     sync.Mutex
	rows   map[string]checkout.Checkout
	getErr error
}

func (r *memRepo) Insert(_ context.Context, c *checkout.Checkout) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[c.ID] = *c
	return nil
}

func (r *memRepo) Get(_ context.Context, id string) (*checkout.Checkout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	c, ok := r.rows[id]
	if !ok {
		return nil, checkout.ErrNotFound
	}
	return &c, nil
}

func (r *memRepo) MarkSubmitting(_ context.Context, id string, b checkout.Binding, payer common.Address, sig []byte, at time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.rows[id]
	if !ok || c.Status != checkout.StatusPending || (c.Bound() && (c.ChainID != b.ChainID || c.Token != b.Token)) {
		return false, nil
	}
	if !c.Bound() {
		c.ChainID, c.Token, c.CreatedBlock = b.ChainID, b.Token, b.CreatedBlock
	}
	c.Status, c.Payer, c.Signature, c.UpdatedAt = checkout.StatusSubmitting, &payer, sig, at
	r.rows[id] = c
	return true, nil
}

func (r *memRepo) Transition(_ context.Context, id string, from checkout.Status, u checkout.Update) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.rows[id]
	if !ok || c.Status != from {
		return false, nil
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

func (r *memRepo) ListActive(context.Context) ([]*checkout.Checkout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*checkout.Checkout
	for _, c := range r.rows {
		if !c.Status.Terminal() {
			out = append(out, &c)
		}
	}
	return out, nil
}

// fakeChain passes every pre-flight unless told otherwise.
type fakeChain struct {
	used    bool
	balance *big.Int // nil means plenty
	simErr  error
	err     error
}

func (f *fakeChain) AuthorizationUsed(context.Context, common.Address, [32]byte) (bool, error) {
	return f.used, f.err
}

func (f *fakeChain) BalanceOf(context.Context, common.Address) (*big.Int, error) {
	if f.balance == nil {
		return big.NewInt(1_000_000_000_000), f.err
	}
	return f.balance, f.err
}

func (f *fakeChain) SimulateTransfer(context.Context, chain.TransferAuthorization, []byte) error {
	if f.err != nil {
		return f.err
	}
	return f.simErr
}

func (f *fakeChain) BlockNumber(context.Context) (uint64, error) { return 1_000, f.err }
func (f *fakeChain) FindAuthorizationUsed(context.Context, *common.Address, [32]byte, uint64) (*chain.AuthorizationLog, error) {
	return nil, nil
}
func (f *fakeChain) Receipt(context.Context, common.Hash) (*types.Receipt, error) { return nil, nil }
func (f *fakeChain) TransactionTo(context.Context, common.Hash) (*common.Address, bool, error) {
	return nil, false, nil
}

func newTestServer(t *testing.T) (*httptest.Server, *memRepo) {
	t.Helper()
	return newTestServerWithChain(t, &fakeChain{})
}

func newTestServerWithChain(t *testing.T, fc *fakeChain) (*httptest.Server, *memRepo) {
	t.Helper()
	repo := &memRepo{rows: map[string]checkout.Checkout{}}
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	sepolia, _ := chain.KnownNetwork("sepolia")
	sepolia.RPCURL = "https://sepolia.invalid"
	networks, err := chain.NewRegistry([]chain.Network{sepolia})
	if err != nil {
		t.Fatal(err)
	}
	svc := checkout.NewService(repo, checkout.Params{
		Merchant:        common.HexToAddress("0x3CC845A0014aFcB0D52F673E0a98039074F1474c"),
		Networks:        networks,
		TTL:             15 * time.Minute,
		MaxAmount:       big.NewInt(1_000_000_000),
		FrontendBaseURL: testOrigin,
	}, func() time.Time { return now }, checkout.Chains{sepolia.ChainID: fc}, checkout.NoopRelayer{})
	srv := httptest.NewServer(NewServer(svc, testAPIKey, testOrigin))
	t.Cleanup(srv.Close)
	return srv, repo
}

func do(t *testing.T, method, url, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Don't follow redirects: a redirect is a response we want to see.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func create(t *testing.T, srv *httptest.Server, body string) (*http.Response, []byte) {
	return do(t, http.MethodPost, srv.URL+"/api/checkouts", body, map[string]string{"X-API-Key": testAPIKey})
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct{ Error, Code string }
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body %q is not JSON: %v", body, err)
	}
	if e.Error == "" {
		t.Errorf("error body %q has no message", body)
	}
	return e.Code
}

func TestCreateRequiresAPIKey(t *testing.T) {
	srv, _ := newTestServer(t)
	body := `{"amount":"12.5","merchant_reference":"ORDER-1"}`

	resp, b := do(t, http.MethodPost, srv.URL+"/api/checkouts", body, nil)
	if resp.StatusCode != 401 || errorCode(t, b) != "unauthorized" {
		t.Errorf("no key: %d %s", resp.StatusCode, b)
	}
	resp, b = do(t, http.MethodPost, srv.URL+"/api/checkouts", body, map[string]string{"X-API-Key": "wrong"})
	if resp.StatusCode != 401 || errorCode(t, b) != "unauthorized" {
		t.Errorf("wrong key: %d %s", resp.StatusCode, b)
	}

	resp, b = create(t, srv, body)
	if resp.StatusCode != 201 {
		t.Fatalf("correct key: %d %s", resp.StatusCode, b)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	id, _ := got["id"].(string)
	if !checkout.ValidID(id) {
		t.Fatalf("id = %v", got["id"])
	}
	want := map[string]any{
		"checkout_url":       testOrigin + "/checkout/" + id,
		"status":             "pending",
		"amount":             "12.5",
		"amount_base_units":  "12500000",
		"merchant":           "0x3CC845A0014aFcB0D52F673E0a98039074F1474c",
		"chain_id":           float64(11155111),
		"merchant_reference": "ORDER-1",
		"created_at":         "2026-10-28T10:00:00Z",
		"expires_at":         "2026-10-28T10:15:00Z",
		"tx_hash":            nil,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
	token, _ := got["token"].(map[string]any)
	if token["symbol"] != "USDC" || token["address"] != "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" || token["decimals"] != float64(6) {
		t.Errorf("token = %#v", got["token"])
	}
	if loc := resp.Header.Get("Location"); loc != "/api/checkouts/"+id {
		t.Errorf("Location = %q", loc)
	}
}

func TestCreateRejectsTampering(t *testing.T) {
	srv, repo := newTestServer(t)
	bodies := map[string]string{
		"merchant":          `{"amount":"1","merchant":"0x84DFa4F646D72251d9D40357f233681148A06cD0"}`,
		"token":             `{"amount":"1","token":"0x0000000000000000000000000000000000000001"}`,
		"amount_base_units": `{"amount":"1","amount_base_units":"1"}`,
		"chain_id":          `{"amount":"1","chain_id":1}`,
		"status":            `{"amount":"1","status":"confirmed"}`,
		"not json":          `amount=1`,
		"two objects":       `{"amount":"1"}{"amount":"2"}`,
		"over 4 KiB":        `{"amount":"1","merchant_reference":"` + strings.Repeat("a", 5000) + `"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			resp, b := create(t, srv, body)
			if resp.StatusCode != 400 || errorCode(t, b) != "invalid_request" {
				t.Errorf("%d %s, want 400 invalid_request", resp.StatusCode, b)
			}
		})
	}
	if len(repo.rows) != 0 {
		t.Errorf("%d checkouts stored from rejected requests", len(repo.rows))
	}
}

func TestCreateInvalidAmount(t *testing.T) {
	srv, _ := newTestServer(t)
	tests := []struct{ body, code string }{
		{`{"amount":"1e3"}`, "invalid_amount"},
		{`{"amount":""}`, "invalid_amount"},
		{`{}`, "invalid_amount"},
		{`{"amount":"1000.000001"}`, "amount_too_large"},
		{`{"amount":12.5}`, "invalid_request"},
		{`{"amount":"1","merchant_reference":"bad\u0007ref"}`, "invalid_reference"},
	}
	for _, tt := range tests {
		resp, b := create(t, srv, tt.body)
		if resp.StatusCode != 400 || errorCode(t, b) != tt.code {
			t.Errorf("%s: %d %s, want 400 %s", tt.body, resp.StatusCode, b, tt.code)
		}
	}
}

func TestGetCheckout(t *testing.T) {
	srv, _ := newTestServer(t)
	_, created := create(t, srv, `{"amount":"1.25"}`)
	var c struct{ ID string }
	if err := json.Unmarshal(created, &c); err != nil {
		t.Fatal(err)
	}

	resp, b := do(t, http.MethodGet, srv.URL+"/api/checkouts/"+c.ID, "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET: %d %s", resp.StatusCode, b)
	}
	if string(b) != string(created) {
		t.Errorf("GET body differs from create body:\n%s\n%s", b, created)
	}

	for _, path := range []string{
		"/api/checkouts/co_AAAAAAAAAAAAAAAAAAAAAA",
		"/api/checkouts/..%2Fx",
		"/api/checkouts/" + strings.Repeat("a", 500),
	} {
		resp, b := do(t, http.MethodGet, srv.URL+path, "", nil)
		if resp.StatusCode != 404 || errorCode(t, b) != "not_found" {
			t.Errorf("GET %s: %d %s, want 404 not_found", path, resp.StatusCode, b)
		}
	}
}

func TestCORS(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, _ := do(t, http.MethodOptions, srv.URL+"/api/checkouts/x", "", map[string]string{
		"Origin":                        testOrigin,
		"Access-Control-Request-Method": "GET",
	})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("allowed origin: Access-Control-Allow-Origin = %q", got)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", resp.StatusCode)
	}

	resp, _ = do(t, http.MethodOptions, srv.URL+"/api/checkouts/x", "", map[string]string{"Origin": "https://evil.example"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("other origin: Access-Control-Allow-Origin = %q, want none", got)
	}
}

func TestInternalErrorHidesDetails(t *testing.T) {
	srv, repo := newTestServer(t)
	repo.getErr = errors.New("pg: secret detail")
	resp, b := do(t, http.MethodGet, srv.URL+"/api/checkouts/co_AAAAAAAAAAAAAAAAAAAAAA", "", nil)
	if resp.StatusCode != 500 || errorCode(t, b) != "internal" {
		t.Errorf("%d %s, want 500 internal", resp.StatusCode, b)
	}
	if strings.Contains(string(b), "secret") {
		t.Errorf("body leaks internal error: %s", b)
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	if resp, _ := do(t, http.MethodGet, srv.URL+"/healthz", "", nil); resp.StatusCode != 200 {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
}
