package chain

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

func testAuthorization(from common.Address) TransferAuthorization {
	var nonce [32]byte
	nonce[0], nonce[31] = 0x42, 0x99
	return TransferAuthorization{
		From:        from,
		To:          common.HexToAddress("0x3CC845A0014aFcB0D52F673E0a98039074F1474c"),
		Value:       big.NewInt(12_500_000),
		ValidAfter:  big.NewInt(0),
		ValidBefore: big.NewInt(1_793_000_000),
		Nonce:       nonce,
	}
}

// sign returns a 65-byte r‖s‖v signature with v in {27, 28}.
func sign(t *testing.T, key *ecdsa.PrivateKey, digest common.Hash) []byte {
	t.Helper()
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return sig
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestDigest_MatchesGoEthereumTypedData(t *testing.T) {
	a := testAuthorization(common.HexToAddress("0x00000000000000000000000000000000000000aa"))
	td := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"}, {Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"},
			},
			"TransferWithAuthorization": {
				{Name: "from", Type: "address"}, {Name: "to", Type: "address"},
				{Name: "value", Type: "uint256"}, {Name: "validAfter", Type: "uint256"},
				{Name: "validBefore", Type: "uint256"}, {Name: "nonce", Type: "bytes32"},
			},
		},
		PrimaryType: "TransferWithAuthorization",
		Domain: apitypes.TypedDataDomain{
			Name: "USDC", Version: "2",
			ChainId:           math.NewHexOrDecimal256(11155111),
			VerifyingContract: sepoliaUSDC.Hex(),
		},
		Message: apitypes.TypedDataMessage{
			"from":        a.From.Hex(),
			"to":          a.To.Hex(),
			"value":       a.Value.String(),
			"validAfter":  a.ValidAfter.String(),
			"validBefore": a.ValidBefore.String(),
			"nonce":       hexutil.Encode(a.Nonce[:]),
		},
	}
	want, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		t.Fatal(err)
	}
	got := a.Digest(DomainSeparator("USDC", "2", 11155111, sepoliaUSDC))
	if got != common.BytesToHash(want) {
		t.Fatalf("Digest = %s, go-ethereum = 0x%x", got, want)
	}
}

func TestRecover_RoundTrip(t *testing.T) {
	key := newKey(t)
	from := crypto.PubkeyToAddress(key.PublicKey)
	d := testAuthorization(from).Digest(sepoliaUSDCDomain)

	got, err := RecoverSigner(d, sign(t, key, d))
	if err != nil {
		t.Fatalf("RecoverSigner: %v", err)
	}
	if got != from {
		t.Fatalf("recovered %s, want %s", got, from)
	}
}

func TestRecover_TamperedValue_DifferentSigner(t *testing.T) {
	key := newKey(t)
	a := testAuthorization(crypto.PubkeyToAddress(key.PublicKey))
	sig := sign(t, key, a.Digest(sepoliaUSDCDomain))

	a.Value = new(big.Int).Add(a.Value, big.NewInt(1))
	got, err := RecoverSigner(a.Digest(sepoliaUSDCDomain), sig)
	if err != nil {
		t.Fatalf("RecoverSigner: %v", err)
	}
	if got == a.From {
		t.Fatal("signature over a different value recovered the payer")
	}
}

func TestRecover_WrongDomain_DifferentSigner(t *testing.T) {
	key := newKey(t)
	a := testAuthorization(crypto.PubkeyToAddress(key.PublicKey))
	for name, domain := range map[string]common.Hash{
		"mainnet chain id": DomainSeparator("USDC", "2", 1, sepoliaUSDC),
		"other token":      DomainSeparator("USDC", "2", 11155111, common.HexToAddress("0x1234")),
	} {
		got, err := RecoverSigner(a.Digest(sepoliaUSDCDomain), sign(t, key, a.Digest(domain)))
		if err != nil {
			t.Fatalf("%s: RecoverSigner: %v", name, err)
		}
		if got == a.From {
			t.Errorf("%s: signature under another domain recovered the payer", name)
		}
	}
}

func TestRecover_RejectsBadV(t *testing.T) {
	key := newKey(t)
	d := testAuthorization(crypto.PubkeyToAddress(key.PublicKey)).Digest(sepoliaUSDCDomain)
	sig := sign(t, key, d)
	for _, v := range []byte{0, 1, 29} {
		bad := append([]byte(nil), sig...)
		bad[64] = v
		if _, err := RecoverSigner(d, bad); !errors.Is(err, ErrSignatureV) {
			t.Errorf("v=%d: err = %v, want ErrSignatureV", v, err)
		}
	}
}

func TestRecover_RejectsHighS(t *testing.T) {
	key := newKey(t)
	d := testAuthorization(crypto.PubkeyToAddress(key.PublicKey)).Digest(sepoliaUSDCDomain)
	sig := sign(t, key, d)

	// (r, n−s) with the other v is the same signature's malleable twin.
	s := new(big.Int).SetBytes(sig[32:64])
	highS := new(big.Int).Sub(crypto.S256().Params().N, s)
	bad := append([]byte(nil), sig...)
	copy(bad[32:64], common.LeftPadBytes(highS.Bytes(), 32))
	bad[64] = 27 + 28 - sig[64]

	if _, err := RecoverSigner(d, bad); !errors.Is(err, ErrSignatureHighS) {
		t.Fatalf("err = %v, want ErrSignatureHighS", err)
	}
}

func TestRecover_RejectsLength(t *testing.T) {
	for _, n := range []int{0, 64, 66} {
		if _, err := RecoverSigner(common.Hash{}, make([]byte, n)); !errors.Is(err, ErrSignatureLength) {
			t.Errorf("len %d: err = %v, want ErrSignatureLength", n, err)
		}
	}
}
