package httpapi

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
)

var testDomain = chain.DomainSeparator("USDC", "2", 11155111, common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"))

// newPending creates a checkout through the API and returns its id.
func newPending(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, b := create(t, srv, `{"amount":"12.50"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, b)
	}
	var c struct{ ID string }
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// signFor returns a valid {payer, signature} body for checkout id.
func signFor(t *testing.T, repo *memRepo, id string, key *ecdsa.PrivateKey) (common.Address, string) {
	t.Helper()
	payer := crypto.PubkeyToAddress(key.PublicKey)
	c := repo.rows[id]
	sig, err := crypto.Sign(c.Authorization(payer).Digest(testDomain).Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return payer, hexutil.Encode(sig)
}

func authBody(payer, sig string) string {
	return fmt.Sprintf(`{"payer":%q,"signature":%q}`, payer, sig)
}

func postAuth(t *testing.T, srv *httptest.Server, id, body string) (*http.Response, []byte) {
	return do(t, http.MethodPost, srv.URL+"/api/checkouts/"+id+"/authorization", body, nil)
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAuthorization_Accepted202(t *testing.T) {
	srv, repo := newTestServer(t)
	id := newPending(t, srv)
	payer, sig := signFor(t, repo, id, newKey(t))

	resp, b := postAuth(t, srv, id, authBody(payer.Hex(), sig))
	if resp.StatusCode != 202 {
		t.Fatalf("POST: %d %s", resp.StatusCode, b)
	}
	var c struct{ ID, Status string }
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.ID != id || c.Status != "submitting" {
		t.Fatalf("body = %s", b)
	}
}

func TestAuthorization_StatusMapping(t *testing.T) {
	key := newKey(t)
	for _, tc := range []struct {
		name   string
		chain  fakeChain
		setup  func(c *checkout.Checkout)                    // edits the stored row
		body   func(payer common.Address, sig string) string // nil = valid body
		id     string                                        // "" = the created checkout
		status int
		code   string
	}{
		{name: "bad json", body: func(common.Address, string) string { return `{"payer":` }, status: 400, code: "invalid_request"},
		{name: "two json values", body: func(p common.Address, s string) string { return authBody(p.Hex(), s) + "{}" }, status: 400, code: "invalid_request"},
		{name: "signer is not payer", body: func(_ common.Address, s string) string {
			return authBody("0x00000000000000000000000000000000000000aa", s)
		}, status: 400, code: "invalid_signature"},
		{name: "short signature", body: func(p common.Address, s string) string { return authBody(p.Hex(), s[:len(s)-2]) }, status: 400, code: "invalid_signature"},
		{name: "unknown id", id: "co_AAAAAAAAAAAAAAAAAAAAAA", status: 404, code: "not_found"},
		{name: "malformed id", id: "nope", status: 404, code: "not_found"},
		{name: "not pending", setup: func(c *checkout.Checkout) { c.Status = checkout.StatusConfirmed }, status: 409, code: "not_pending"},
		{name: "inside safety margin", setup: func(c *checkout.Checkout) { c.ExpiresAt = time.Date(2026, 10, 28, 10, 0, 59, 0, time.UTC) }, status: 409, code: "expired"},
		{name: "insufficient balance", chain: fakeChain{balance: big.NewInt(1)}, status: 422, code: "insufficient_balance"},
		{name: "simulation reverts", chain: fakeChain{simErr: &chain.RevertError{Reason: "FiatTokenV2: invalid signature"}}, status: 422, code: "simulation_failed"},
		{name: "rpc down", chain: fakeChain{err: errors.New("dial tcp: connection refused")}, status: 503, code: "chain_unavailable"},
		{name: "nonce already used on chain", chain: fakeChain{used: true}, status: 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := tc.chain
			chainErr := fc.err
			fc.err = nil // the chain fails only after the checkout exists
			srv, repo := newTestServerWithChain(t, &fc)
			id := newPending(t, srv)
			fc.err = chainErr
			if tc.setup != nil {
				c := repo.rows[id]
				tc.setup(&c)
				repo.rows[id] = c
			}
			payer, sig := signFor(t, repo, id, key)
			body := authBody(payer.Hex(), sig)
			if tc.body != nil {
				body = tc.body(payer, sig)
			}
			if tc.id != "" {
				id = tc.id
			}

			resp, b := postAuth(t, srv, id, body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d %s, want %d", resp.StatusCode, b, tc.status)
			}
			if tc.code != "" && errorCode(t, b) != tc.code {
				t.Fatalf("code in %s, want %s", b, tc.code)
			}
			if tc.code == "simulation_failed" && !strings.Contains(string(b), "FiatTokenV2: invalid signature") {
				t.Errorf("body %s does not carry the revert reason", b)
			}
			if tc.code == "chain_unavailable" && strings.Contains(string(b), "dial tcp") {
				t.Errorf("body %s leaks the RPC error", b)
			}
		})
	}
}

func TestAuthorization_SecondPostConflicts(t *testing.T) {
	srv, repo := newTestServer(t)
	id := newPending(t, srv)
	payer, sig := signFor(t, repo, id, newKey(t))
	if resp, b := postAuth(t, srv, id, authBody(payer.Hex(), sig)); resp.StatusCode != 202 {
		t.Fatalf("first: %d %s", resp.StatusCode, b)
	}
	resp, b := postAuth(t, srv, id, authBody(payer.Hex(), sig))
	if resp.StatusCode != 409 || errorCode(t, b) != "not_pending" {
		t.Fatalf("second: %d %s, want 409 not_pending", resp.StatusCode, b)
	}
}

func TestAuthorization_UnknownFieldsRejected(t *testing.T) {
	srv, repo := newTestServer(t)
	id := newPending(t, srv)
	payer, sig := signFor(t, repo, id, newKey(t))
	for _, extra := range []string{`"amount":"0.01"`, `"to":"0x00000000000000000000000000000000000000aa"`, `"nonce":"0x00"`} {
		body := fmt.Sprintf(`{"payer":%q,"signature":%q,%s}`, payer.Hex(), sig, extra)
		resp, b := postAuth(t, srv, id, body)
		if resp.StatusCode != 400 || errorCode(t, b) != "invalid_request" {
			t.Errorf("%s: %d %s, want 400 invalid_request", extra, resp.StatusCode, b)
		}
	}
	if repo.rows[id].Status != checkout.StatusPending {
		t.Fatal("a rejected body changed the checkout")
	}
}

func TestAuthorization_BadHex(t *testing.T) {
	srv, repo := newTestServer(t)
	id := newPending(t, srv)
	payer, sig := signFor(t, repo, id, newKey(t))
	for name, body := range map[string]string{
		"payer 19 bytes":    authBody(payer.Hex()[:40], sig),
		"payer without 0x":  authBody(payer.Hex()[2:], sig),
		"payer missing":     fmt.Sprintf(`{"signature":%q}`, sig),
		"signature no 0x":   authBody(payer.Hex(), sig[2:]),
		"signature odd hex": authBody(payer.Hex(), sig+"0"),
		"signature not hex": authBody(payer.Hex(), "0xzz"),
		"signature missing": fmt.Sprintf(`{"payer":%q}`, payer.Hex()),
	} {
		resp, b := postAuth(t, srv, id, body)
		if resp.StatusCode != 400 || errorCode(t, b) != "invalid_request" {
			t.Errorf("%s: %d %s, want 400 invalid_request", name, resp.StatusCode, b)
		}
	}
}

func TestAuthorization_PayerCaseInsensitive(t *testing.T) {
	srv, repo := newTestServer(t)
	key := newKey(t)
	for _, format := range []func(common.Address) string{
		func(a common.Address) string { return strings.ToLower(a.Hex()) },
		func(a common.Address) string { return a.Hex() },
		func(a common.Address) string { return "0x" + strings.ToUpper(a.Hex()[2:]) },
	} {
		id := newPending(t, srv)
		payer, sig := signFor(t, repo, id, key)
		if resp, b := postAuth(t, srv, id, authBody(format(payer), sig)); resp.StatusCode != 202 {
			t.Errorf("payer %s: %d %s, want 202", format(payer), resp.StatusCode, b)
		}
	}
}

func TestAuthorization_CORSPreflightAllowsPost(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, _ := do(t, http.MethodOptions, srv.URL+"/api/checkouts/co_AAAAAAAAAAAAAAAAAAAAAA/authorization", "", map[string]string{
		"Origin":                         testOrigin,
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "content-type",
	})
	h := resp.Header
	if resp.StatusCode != 204 || h.Get("Access-Control-Allow-Origin") != testOrigin ||
		!strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Fatalf("preflight: %d %v", resp.StatusCode, h)
	}
}

func TestCreate_ChainDown503(t *testing.T) {
	srv, _ := newTestServerWithChain(t, &fakeChain{err: errors.New("dial tcp: refused")})
	resp, b := create(t, srv, `{"amount":"1"}`)
	if resp.StatusCode != 503 || errorCode(t, b) != "chain_unavailable" || strings.Contains(string(b), "dial") {
		t.Fatalf("create with the chain down: %d %s", resp.StatusCode, b)
	}
}

// A row whose chain is not enabled (e.g. CHAINS changed while it was
// pending) can be neither signed nor shown: 409 on authorize, 500 on GET.
func TestAuthorization_UnsupportedChain409(t *testing.T) {
	srv, repo := newTestServer(t)
	id := newPending(t, srv)
	payer, sig := signFor(t, repo, id, newKey(t))
	amoy, _ := chain.KnownNetwork("amoy")
	row := repo.rows[id]
	row.ChainID, row.Token = amoy.ChainID, amoy.USDC
	repo.rows[id] = row

	resp, b := postAuth(t, srv, id, authBody(payer.Hex(), sig))
	if resp.StatusCode != 409 || !strings.Contains(string(b), "unsupported_chain") {
		t.Fatalf("POST: %d %s, want 409 unsupported_chain", resp.StatusCode, b)
	}
	if resp, b := do(t, http.MethodGet, srv.URL+"/api/checkouts/"+id, "", nil); resp.StatusCode != 500 {
		t.Fatalf("GET: %d %s, want 500", resp.StatusCode, b)
	}
}
