package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

// The frontend passes typed_data (plus message.from) to MetaMask unchanged,
// so an independent EIP-712 implementation must hash it to the digest the
// server verifies. This is the contract between frontend and backend.
func TestGet_TypedDataSignsToServerDigest(t *testing.T) {
	srv, repo := newTestServer(t)
	_, created := create(t, srv, `{"amount":"12.50"}`)
	var body struct {
		ID        string
		TypedData json.RawMessage `json:"typed_data"`
	}
	if err := json.Unmarshal(created, &body); err != nil {
		t.Fatal(err)
	}

	var shape struct {
		Domain      map[string]any                 `json:"domain"`
		Types       map[string][]map[string]string `json:"types"`
		PrimaryType string                         `json:"primaryType"`
		Message     map[string]any                 `json:"message"`
	}
	if err := json.Unmarshal(body.TypedData, &shape); err != nil {
		t.Fatal(err)
	}
	if shape.PrimaryType != "TransferWithAuthorization" || len(shape.Types["TransferWithAuthorization"]) != 6 {
		t.Errorf("types = %v / %s", shape.Types, shape.PrimaryType)
	}
	if _, ok := shape.Message["from"]; ok {
		t.Error("message.from must be absent; the frontend adds it")
	}
	for _, k := range []string{"value", "validAfter", "validBefore"} {
		if _, ok := shape.Message[k].(string); !ok {
			t.Errorf("message.%s = %#v, want a decimal string", k, shape.Message[k])
		}
	}
	if shape.Message["value"] != "12500000" {
		t.Errorf("message.value = %v", shape.Message["value"])
	}

	from := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	var td apitypes.TypedData
	if err := json.Unmarshal(body.TypedData, &td); err != nil {
		t.Fatal(err)
	}
	td.Types["EIP712Domain"] = []apitypes.Type{
		{Name: "name", Type: "string"}, {Name: "version", Type: "string"},
		{Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"},
	}
	td.Message["from"] = from.Hex()
	got, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		t.Fatalf("hashing typed_data: %v", err)
	}

	// Unbound, so the typed data is for the primary network (Sepolia).
	c := repo.rows[body.ID]
	domain := chain.DomainSeparator("USDC", "2", 11155111, common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"))
	if want := c.Authorization(from).Digest(domain); common.BytesToHash(got) != want {
		t.Fatalf("typed_data hashes to 0x%x, server digest is %s", got, want)
	}

	resp, b := do(t, http.MethodGet, srv.URL+"/api/checkouts/"+body.ID, "", nil)
	if resp.StatusCode != 200 || string(b) != string(created) {
		t.Errorf("GET differs from create: %d %s", resp.StatusCode, b)
	}
}
