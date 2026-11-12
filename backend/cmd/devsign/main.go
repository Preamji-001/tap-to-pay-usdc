// Command devsign signs a checkout's typed_data the way MetaMask would, so
// the authorization endpoint can be tried with curl before the frontend
// exists. Use a throwaway key only.
//
//	DEVSIGN_KEY=$(openssl rand -hex 32) go run ./cmd/devsign co_… \
//	  | curl -s -XPOST localhost:8080/api/checkouts/co_…/authorization -d @-
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devsign:", err)
		os.Exit(1)
	}
}

func run() error {
	api := flag.String("api", "http://localhost:8080", "backend base URL")
	tamper := flag.Bool("tamper", false, "flip a byte of the signature (expect 400 invalid_signature)")
	flag.Parse()
	if flag.NArg() != 1 {
		return errors.New("usage: DEVSIGN_KEY=<hex> devsign [-api URL] [-tamper] <checkout id>")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(os.Getenv("DEVSIGN_KEY"), "0x"))
	if err != nil {
		return errors.New("DEVSIGN_KEY must be a 32-byte hex private key")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(strings.TrimRight(*api, "/") + "/api/checkouts/" + flag.Arg(0))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET checkout: %s", resp.Status)
	}
	var body struct {
		TypedData apitypes.TypedData `json:"typed_data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decoding checkout: %w", err)
	}

	// What a wallet does: add the domain type and the signer, then hash.
	td := body.TypedData
	payer := crypto.PubkeyToAddress(key.PublicKey)
	td.Types["EIP712Domain"] = []apitypes.Type{
		{Name: "name", Type: "string"}, {Name: "version", Type: "string"},
		{Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"},
	}
	td.Message["from"] = payer.Hex()
	digest, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		return fmt.Errorf("hashing typed_data: %w", err)
	}
	sig, err := crypto.Sign(digest, key)
	if err != nil {
		return err
	}
	sig[64] += 27
	if *tamper {
		sig[10] ^= 0xff
	}

	return json.NewEncoder(os.Stdout).Encode(map[string]string{
		"payer":     payer.Hex(),
		"signature": hexutil.Encode(sig),
	})
}
