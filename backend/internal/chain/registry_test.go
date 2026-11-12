package chain

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// DOMAIN_SEPARATOR() values read on-chain on 2026-11-11 with
// `cast call <usdc> "DOMAIN_SEPARATOR()(bytes32)"`. If the registry's chain
// ID, address, name or version drifts, the computed separator stops matching.
var onChainDomains = map[string]string{
	"base-sepolia": "0x71f17a3b2ff373b803d70a5a07c046c1a2bc8e89c09ef722fcb047abe94c9818",
	"amoy":         "0x5cddc98319864e897e4469bd1d13c2288c677047b54b8cca12bd342adb6be9eb",
}

func TestKnownNetworks(t *testing.T) {
	want := map[string]struct {
		id   int64
		usdc string
		gas  string
	}{
		// Addresses from developers.circle.com/stablecoins/usdc-contract-addresses (testnet table).
		"sepolia":      {11155111, "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", "ETH"},
		"base-sepolia": {84532, "0x036CbD53842c5426634e7929541eC2318f3dCF7e", "ETH"},
		"amoy":         {80002, "0x41E94Eb019C0762f9Bfcf9Fb1E58725BfB0e7582", "POL"},
	}
	if got := KnownNetworkKeys(); len(got) != len(want) {
		t.Fatalf("KnownNetworkKeys = %v, want %d networks", got, len(want))
	}
	for key, w := range want {
		n, ok := KnownNetwork(key)
		if !ok {
			t.Fatalf("KnownNetwork(%q) missing", key)
		}
		if n.Key != key || n.ChainID != w.id || n.USDC != common.HexToAddress(w.usdc) || n.GasToken != w.gas {
			t.Errorf("%s = %+v", key, n)
		}
		if n.Decimals != USDCDecimals || n.EIP712Name != "USDC" || n.EIP712Version != "2" {
			t.Errorf("%s token facts = %d %q %q", key, n.Decimals, n.EIP712Name, n.EIP712Version)
		}
		if n.Confirmations < 1 || n.ExplorerURL == "" || n.Name == "" {
			t.Errorf("%s missing defaults: %+v", key, n)
		}
		if n.RPCURL != "" {
			t.Errorf("%s: the static entry must not carry an RPC URL", key)
		}
	}
	if _, ok := KnownNetwork("mainnet"); ok {
		t.Error("KnownNetwork(mainnet) should not exist: testnets only")
	}
}

func TestKnownNetworkReturnsCopy(t *testing.T) {
	n, _ := KnownNetwork("sepolia")
	n.ChainID = 1
	again, _ := KnownNetwork("sepolia")
	if again.ChainID != 11155111 {
		t.Fatal("mutating a returned Network changed the built-in table")
	}
}

func TestKnownDomainsMatchChain(t *testing.T) {
	for key, hex := range onChainDomains {
		n, _ := KnownNetwork(key)
		p := n.TokenParams()
		got := DomainSeparator(p.Name, p.Version, p.ChainID, p.Address)
		if got != common.HexToHash(hex) {
			t.Errorf("%s: computed %s, on-chain %s", key, got.Hex(), hex)
		}
	}
	// Sepolia's value is pinned in domain_test.go.
	s, _ := KnownNetwork("sepolia")
	if p := s.TokenParams(); DomainSeparator(p.Name, p.Version, p.ChainID, p.Address) != sepoliaUSDCDomain {
		t.Error("sepolia: computed separator differs from the pinned one")
	}
}

func withRPC(key, rpc string) Network {
	n, _ := KnownNetwork(key)
	n.RPCURL = rpc
	return n
}

func TestNewRegistry(t *testing.T) {
	reg, err := NewRegistry([]Network{withRPC("base-sepolia", "https://b"), withRPC("sepolia", "https://s")})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if reg.Primary().Key != "base-sepolia" {
		t.Errorf("Primary = %s, want the first network", reg.Primary().Key)
	}
	if n, ok := reg.ByID(11155111); !ok || n.Key != "sepolia" {
		t.Errorf("ByID(11155111) = %+v, %v", n, ok)
	}
	if _, ok := reg.ByID(80002); ok {
		t.Error("ByID(80002) found a network that is not enabled")
	}
	if got := len(reg.Networks()); got != 2 {
		t.Errorf("Networks has %d entries", got)
	}
}

func TestNewRegistryRejects(t *testing.T) {
	sixDecimals := withRPC("amoy", "https://a")
	sixDecimals.Decimals = 18
	noConfirmations := withRPC("amoy", "https://a")
	noConfirmations.Confirmations = 0
	zeroToken := withRPC("amoy", "https://a")
	zeroToken.USDC = common.Address{}

	tests := []struct {
		name string
		nets []Network
		want string
	}{
		{"empty", nil, "at least one"},
		{"duplicate chain", []Network{withRPC("amoy", "https://a"), withRPC("amoy", "https://b")}, "twice"},
		{"no rpc", []Network{withRPC("amoy", "")}, "RPC"},
		{"18 decimals", []Network{sixDecimals}, "decimals"},
		{"zero confirmations", []Network{noConfirmations}, "confirmations"},
		{"zero token", []Network{zeroToken}, "USDC address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRegistry(tt.nets)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewRegistry = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

// selectorReader answers DOMAIN_SEPARATOR() and decimals() by selector.
type selectorReader struct {
	chainID  int64
	code     []byte
	domain   []byte
	decimals []byte
}

func (f selectorReader) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(f.chainID), nil
}

func (f selectorReader) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return f.code, nil
}

func (f selectorReader) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	switch common.Bytes2Hex(msg.Data) {
	case "3644e515":
		return f.domain, nil
	case "313ce567":
		return f.decimals, nil
	}
	return nil, errors.New("unexpected call data " + common.Bytes2Hex(msg.Data))
}

func goodBaseReader() selectorReader {
	return selectorReader{
		chainID:  84532,
		code:     []byte{0x60, 0x80},
		domain:   common.HexToHash(onChainDomains["base-sepolia"]).Bytes(),
		decimals: common.LeftPadBytes([]byte{6}, 32),
	}
}

func TestCheckNetwork(t *testing.T) {
	base := withRPC("base-sepolia", "https://b")
	tests := []struct {
		name   string
		mutate func(*selectorReader, *Network)
		want   string
	}{
		{name: "all good", mutate: func(*selectorReader, *Network) {}},
		{"18 decimals on chain", func(r *selectorReader, _ *Network) { r.decimals = common.LeftPadBytes([]byte{18}, 32) }, "decimals"},
		{"short decimals reply", func(r *selectorReader, _ *Network) { r.decimals = []byte{6} }, "decimals"},
		{"wrong usdc address", func(r *selectorReader, n *Network) {
			n.USDC = common.HexToAddress("0x41E94Eb019C0762f9Bfcf9Fb1E58725BfB0e7582")
		}, "domain separator"},
		{"rpc on another chain", func(r *selectorReader, _ *Network) { r.chainID = 80002 }, "chain id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, n := goodBaseReader(), base
			tt.mutate(&r, &n)
			err := CheckNetwork(context.Background(), r, n)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("CheckNetwork: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CheckNetwork = %v, want error containing %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "base-sepolia") {
				t.Errorf("error %q does not name the network", err)
			}
		})
	}
}

func TestNetworkDomainSeparator(t *testing.T) {
	for key, hex := range onChainDomains {
		n, _ := KnownNetwork(key)
		if got := n.DomainSeparator(); got != common.HexToHash(hex) {
			t.Errorf("%s: DomainSeparator = %s, want %s", key, got.Hex(), hex)
		}
	}
}

// A signature made for one chain, checked under another chain's domain,
// does not fail to recover: it recovers a different, meaningless address.
// That is why Authorize compares the recovered signer with the payer.
func TestCrossChainSignatureRecoversStranger(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	payer := crypto.PubkeyToAddress(key.PublicKey)
	sepolia, _ := KnownNetwork("sepolia")
	base, _ := KnownNetwork("base-sepolia")

	a := TransferAuthorization{
		From: payer, To: common.HexToAddress("0x3CC845A0014aFcB0D52F673E0a98039074F1474c"),
		Value: big.NewInt(1_250_000), ValidAfter: big.NewInt(0), ValidBefore: big.NewInt(1_900_000_000),
		Nonce: [32]byte{1, 2, 3},
	}
	if a.Digest(sepolia.DomainSeparator()) == a.Digest(base.DomainSeparator()) {
		t.Fatal("the same authorization has the same digest on two chains")
	}

	sig, err := crypto.Sign(a.Digest(sepolia.DomainSeparator()).Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27

	if got, err := RecoverSigner(a.Digest(sepolia.DomainSeparator()), sig); err != nil || got != payer {
		t.Fatalf("on its own chain: %s, %v; want %s", got.Hex(), err, payer.Hex())
	}
	got, err := RecoverSigner(a.Digest(base.DomainSeparator()), sig)
	if err != nil {
		t.Fatalf("RecoverSigner on the other chain errored (%v); it should return some other address", err)
	}
	if got == payer {
		t.Fatal("a Sepolia signature recovered the payer under the Base domain")
	}
}
