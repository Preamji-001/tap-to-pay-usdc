package httpapi

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
)

// L4 over HTTP: ?chain= on GET, chain_id on authorization, and routes.

// newTwoChainServer enables Sepolia (primary) and Base Sepolia, each with its
// own fake chain.
func newTwoChainServer(t *testing.T) (*httptest.Server, *memRepo, map[string]*fakeChain) {
	t.Helper()
	repo := &memRepo{rows: map[string]checkout.Checkout{}}
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	var nets []chain.Network
	fakes, chains := map[string]*fakeChain{}, checkout.Chains{}
	for _, k := range []string{"sepolia", "base-sepolia"} {
		n, _ := chain.KnownNetwork(k)
		n.RPCURL = "https://" + k + ".invalid"
		nets = append(nets, n)
		fakes[k] = &fakeChain{}
		chains[n.ChainID] = fakes[k]
	}
	reg, err := chain.NewRegistry(nets)
	if err != nil {
		t.Fatal(err)
	}
	svc := checkout.NewService(repo, checkout.Params{
		Merchant: common.HexToAddress("0x3CC845A0014aFcB0D52F673E0a98039074F1474c"), Networks: reg,
		TTL: 15 * time.Minute, MaxAmount: big.NewInt(1_000_000_000), FrontendBaseURL: testOrigin,
	}, func() time.Time { return now }, chains, checkout.NoopRelayer{})
	srv := httptest.NewServer(NewServer(svc, testAPIKey, testOrigin))
	t.Cleanup(srv.Close)
	return srv, repo, fakes
}

var (
	baseUSDC   = common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e")
	baseDomain = chain.DomainSeparator("USDC", "2", 84532, baseUSDC)
)

type viewJSON struct {
	ChainID    int64 `json:"chain_id"`
	ChainBound bool  `json:"chain_bound"`
	Token      struct {
		Address string `json:"address"`
	} `json:"token"`
	TypedData struct {
		Domain struct {
			ChainID           int64  `json:"chainId"`
			VerifyingContract string `json:"verifyingContract"`
		} `json:"domain"`
	} `json:"typed_data"`
}

func getView(t *testing.T, srv *httptest.Server, path string) (int, viewJSON, []byte) {
	t.Helper()
	resp, b := do(t, http.MethodGet, srv.URL+path, "", nil)
	var v viewJSON
	if resp.StatusCode == 200 {
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, v, b
}

func signOn(t *testing.T, repo *memRepo, id string, key *ecdsa.PrivateKey, domain common.Hash) (common.Address, string) {
	t.Helper()
	payer := crypto.PubkeyToAddress(key.PublicKey)
	c := repo.rows[id]
	sig, err := crypto.Sign(c.Authorization(payer).Digest(domain).Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return payer, hexutil.Encode(sig)
}

func TestGet_UnboundDefaultsToPrimary_ChainQueryPicksAnother(t *testing.T) {
	srv, _, _ := newTwoChainServer(t)
	id := newPending(t, srv)

	code, v, b := getView(t, srv, "/api/checkouts/"+id)
	if code != 200 || v.ChainBound || v.ChainID != 11155111 || v.TypedData.Domain.ChainID != 11155111 {
		t.Fatalf("default view: %d %s", code, b)
	}
	code, v, b = getView(t, srv, "/api/checkouts/"+id+"?chain=84532")
	if code != 200 || v.ChainBound || v.ChainID != 84532 || v.TypedData.Domain.ChainID != 84532 ||
		v.TypedData.Domain.VerifyingContract != baseUSDC.Hex() || v.Token.Address != baseUSDC.Hex() {
		t.Fatalf("?chain=84532: %d %s", code, b)
	}
}

func TestGet_ChainQueryErrors(t *testing.T) {
	srv, _, _ := newTwoChainServer(t)
	id := newPending(t, srv)
	for q, want := range map[string]string{
		"1": "unknown_chain", "80002": "unknown_chain",
		"0": "invalid_request", "-5": "invalid_request", "abc": "invalid_request", "0x14a34": "invalid_request",
	} {
		code, _, b := getView(t, srv, "/api/checkouts/"+id+"?chain="+q)
		if code != 400 || errorCode(t, b) != want {
			t.Errorf("?chain=%s: %d %s, want 400 %s", q, code, b, want)
		}
	}
}

func TestAuthorization_ChainIDBindsAndSticks(t *testing.T) {
	srv, repo, _ := newTwoChainServer(t)
	id := newPending(t, srv)
	payer, sig := signOn(t, repo, id, newKey(t), baseDomain)

	resp, b := postAuth(t, srv, id, fmt.Sprintf(`{"payer":%q,"signature":%q,"chain_id":84532}`, payer.Hex(), sig))
	if resp.StatusCode != 202 {
		t.Fatalf("POST: %d %s", resp.StatusCode, b)
	}
	var v viewJSON
	if err := json.Unmarshal(b, &v); err != nil || !v.ChainBound || v.ChainID != 84532 {
		t.Fatalf("202 body: %s", b)
	}
	if c := repo.rows[id]; c.ChainID != 84532 || c.Token != baseUSDC {
		t.Fatalf("stored chain %d token %s", c.ChainID, c.Token.Hex())
	}
	if code, v, b := getView(t, srv, "/api/checkouts/"+id); code != 200 || v.ChainID != 84532 || !v.ChainBound {
		t.Fatalf("GET after binding: %d %s", code, b)
	}
	if code, _, b := getView(t, srv, "/api/checkouts/"+id+"?chain=11155111"); code != 409 || errorCode(t, b) != "chain_mismatch" {
		t.Fatalf("GET another chain after binding: %d %s, want 409 chain_mismatch", code, b)
	}
}

func TestAuthorization_ChainErrors(t *testing.T) {
	srv, repo, _ := newTwoChainServer(t)
	id := newPending(t, srv)
	key := newKey(t)
	payer, sepSig := signOn(t, repo, id, key, testDomain)

	for _, tt := range []struct {
		body      string
		code      int
		errorCode string
		reason    string
	}{
		{fmt.Sprintf(`{"payer":%q,"signature":%q,"chain_id":1}`, payer.Hex(), sepSig), 400, "unknown_chain", "mainnet is not enabled"},
		{fmt.Sprintf(`{"payer":%q,"signature":%q,"chain_id":-1}`, payer.Hex(), sepSig), 400, "invalid_request", "negative chain"},
		{fmt.Sprintf(`{"payer":%q,"signature":%q,"chain_id":"84532"}`, payer.Hex(), sepSig), 400, "invalid_request", "chain as a string"},
		{fmt.Sprintf(`{"payer":%q,"signature":%q,"chain_id":84532}`, payer.Hex(), sepSig), 400, "invalid_signature", "Sepolia signature sent as Base"},
	} {
		resp, b := postAuth(t, srv, id, tt.body)
		if resp.StatusCode != tt.code || errorCode(t, b) != tt.errorCode {
			t.Errorf("%s: %d %s, want %d %s", tt.reason, resp.StatusCode, b, tt.code, tt.errorCode)
		}
	}
	if c := repo.rows[id]; c.Status != checkout.StatusPending || c.Bound() {
		t.Fatalf("a rejected authorization changed the checkout: %s bound %v", c.Status, c.Bound())
	}
}

type routesBody struct {
	Amount string `json:"amount"`
	Routes []struct {
		ChainID    int64   `json:"chain_id"`
		Network    string  `json:"network"`
		Balance    *string `json:"balance"`
		Sufficient bool    `json:"sufficient"`
	} `json:"routes"`
	RecommendedChainID *int64 `json:"recommended_chain_id"`
}

func postRoutes(t *testing.T, srv *httptest.Server, id, body string) (*http.Response, []byte) {
	return do(t, http.MethodPost, srv.URL+"/api/checkouts/"+id+"/routes", body, nil)
}

func TestRoutes_Shape(t *testing.T) {
	srv, _, fakes := newTwoChainServer(t)
	id := newPending(t, srv)
	fakes["sepolia"].balance = big.NewInt(20_000_000)
	fakes["base-sepolia"].balance = big.NewInt(3_000_000)

	resp, b := postRoutes(t, srv, id, `{"payer":"0x00000000000000000000000000000000000000aa","preferred_chain_id":84532}`)
	if resp.StatusCode != 200 {
		t.Fatalf("routes: %d %s", resp.StatusCode, b)
	}
	var r routesBody
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Amount != "12.5" || len(r.Routes) != 2 || r.RecommendedChainID == nil || *r.RecommendedChainID != 11155111 {
		t.Fatalf("body %s", b)
	}
	sep, base := r.Routes[0], r.Routes[1]
	if sep.ChainID != 11155111 || sep.Network != "Ethereum Sepolia" || *sep.Balance != "20" || !sep.Sufficient {
		t.Errorf("sepolia route %+v", sep)
	}
	if base.ChainID != 84532 || *base.Balance != "3" || base.Sufficient {
		t.Errorf("base route %+v", base)
	}
}

func TestRoutes_UnknownBalanceIsNull_NoRecommendation(t *testing.T) {
	srv, _, fakes := newTwoChainServer(t)
	id := newPending(t, srv) // created before the chains go down
	fakes["sepolia"].balance = big.NewInt(1)
	fakes["base-sepolia"].err = fmt.Errorf("down")
	resp, b := postRoutes(t, srv, id, `{"payer":"0x00000000000000000000000000000000000000aa"}`)
	var r routesBody
	if resp.StatusCode != 200 || json.Unmarshal(b, &r) != nil {
		t.Fatalf("routes: %d %s", resp.StatusCode, b)
	}
	if r.Routes[1].Balance != nil || r.RecommendedChainID != nil {
		t.Fatalf("want base balance null and no recommendation: %s", b)
	}
}

func TestRoutes_BadRequests(t *testing.T) {
	srv, _, _ := newTwoChainServer(t)
	id := newPending(t, srv)
	for body, want := range map[string]int{
		`{"payer":"0x00000000000000000000000000000000000000aa","amount":"1"}`: 400, // unknown field
		`{"payer":"aa"}`: 400,
		`{}`:             400,
	} {
		if resp, b := postRoutes(t, srv, id, body); resp.StatusCode != want {
			t.Errorf("%s: %d %s, want %d", body, resp.StatusCode, b, want)
		}
	}
	if resp, b := postRoutes(t, srv, "co_nopeAAAAAAAAAAAAAAAAA", `{"payer":"0x00000000000000000000000000000000000000aa"}`); resp.StatusCode != 404 {
		t.Errorf("unknown checkout: %d %s", resp.StatusCode, b)
	}
}
