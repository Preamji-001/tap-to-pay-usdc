package chain

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// USDCDecimals is the only token precision we accept. Amounts are parsed
// and stored in 6-decimal base units, so a token with any other precision
// would silently move the wrong value.
const USDCDecimals = 6

// Network is one chain we can settle USDC on. The static facts come from
// the built-in table below; RPCURL and Confirmations come from config.
type Network struct {
	Key      string // config name, e.g. "base-sepolia"
	Name     string // human name, e.g. "Base Sepolia"
	ChainID  int64
	GasToken string // what the relayer pays gas in: "ETH", "POL"

	// Native Circle USDC on this chain. Bridged variants (e.g. Polygon's
	// USDC.e) are different contracts and are never listed.
	USDC          common.Address
	Decimals      uint8
	EIP712Name    string
	EIP712Version string

	ExplorerURL string // base URL; a tx is at ExplorerURL + "/tx/" + hash

	// FeeRank orders networks by what a relayer send costs on their mainnets:
	// lower is cheaper. When several chains could take a payment, the
	// cheapest one is recommended.
	FeeRank int

	RPCURL        string // never logged: it usually embeds a provider key
	Confirmations uint64 // blocks a receipt must be buried under
}

// TokenParams is the input CheckToken and the EIP-712 domain need.
func (n Network) TokenParams() TokenParams {
	return TokenParams{ChainID: n.ChainID, Address: n.USDC, Name: n.EIP712Name, Version: n.EIP712Version}
}

// DomainSeparator is the EIP-712 domain separator for this network's USDC.
// It commits to the chain ID and the token address, so a signature made for
// one network can never verify on another.
func (n Network) DomainSeparator() common.Hash {
	return DomainSeparator(n.EIP712Name, n.EIP712Version, n.ChainID, n.USDC)
}

// knownNetworks holds testnets only. USDC addresses are from Circle's list
// (developers.circle.com/stablecoins/usdc-contract-addresses). The EIP-712
// name/version and decimals were read on-chain on 2026-11-11, and CheckNetwork
// re-confirms all of it at every startup.
var knownNetworks = []Network{
	{
		Key: "sepolia", Name: "Ethereum Sepolia", ChainID: 11155111, GasToken: "ETH",
		USDC:     common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"),
		Decimals: USDCDecimals, EIP712Name: "USDC", EIP712Version: "2",
		ExplorerURL: "https://sepolia.etherscan.io", Confirmations: 1, FeeRank: 3,
	},
	{
		Key: "base-sepolia", Name: "Base Sepolia", ChainID: 84532, GasToken: "ETH",
		USDC:     common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e"),
		Decimals: USDCDecimals, EIP712Name: "USDC", EIP712Version: "2",
		ExplorerURL: "https://base-sepolia.blockscout.com", Confirmations: 1, FeeRank: 1,
	},
	{
		// Polygon PoS reorgs more often than the others, so wait a little longer.
		Key: "amoy", Name: "Polygon Amoy", ChainID: 80002, GasToken: "POL",
		USDC:     common.HexToAddress("0x41E94Eb019C0762f9Bfcf9Fb1E58725BfB0e7582"),
		Decimals: USDCDecimals, EIP712Name: "USDC", EIP712Version: "2",
		ExplorerURL: "https://amoy.polygonscan.com", Confirmations: 3, FeeRank: 2,
	},
}

// KnownNetwork returns a copy of the built-in entry for key, without an RPC URL.
func KnownNetwork(key string) (Network, bool) {
	for _, n := range knownNetworks {
		if n.Key == key {
			return n, true
		}
	}
	return Network{}, false
}

// KnownNetworkKeys lists the keys CHAINS may name, in table order.
func KnownNetworkKeys() []string {
	keys := make([]string, len(knownNetworks))
	for i, n := range knownNetworks {
		keys[i] = n.Key
	}
	return keys
}

// Registry is the set of networks enabled for this server.
type Registry struct {
	list []Network
	byID map[int64]Network
}

// NewRegistry validates nets and indexes them by chain ID. The first
// network is the primary one.
func NewRegistry(nets []Network) (*Registry, error) {
	if len(nets) == 0 {
		return nil, errors.New("registry needs at least one network")
	}
	r := &Registry{byID: make(map[int64]Network, len(nets))}
	var errs []error
	for _, n := range nets {
		if _, dup := r.byID[n.ChainID]; dup {
			errs = append(errs, fmt.Errorf("%s: chain %d listed twice", n.Key, n.ChainID))
			continue
		}
		if n.RPCURL == "" {
			errs = append(errs, fmt.Errorf("%s: RPC URL is empty", n.Key))
		}
		if n.Decimals != USDCDecimals {
			errs = append(errs, fmt.Errorf("%s: decimals is %d, want %d", n.Key, n.Decimals, USDCDecimals))
		}
		if n.Confirmations < 1 {
			errs = append(errs, fmt.Errorf("%s: confirmations must be at least 1", n.Key))
		}
		if n.USDC == (common.Address{}) {
			errs = append(errs, fmt.Errorf("%s: USDC address is zero", n.Key))
		}
		r.byID[n.ChainID] = n
		r.list = append(r.list, n)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return r, nil
}

// Networks returns the enabled networks in config order.
func (r *Registry) Networks() []Network { return append([]Network(nil), r.list...) }

// Primary is the first enabled network: the chain a checkout is paid on when
// the customer doesn't name one.
func (r *Registry) Primary() Network { return r.list[0] }

// ByID finds an enabled network by chain ID.
func (r *Registry) ByID(id int64) (Network, bool) {
	n, ok := r.byID[id]
	return n, ok
}

// selector of decimals()
var decimalsSelector = []byte{0x31, 0x3c, 0xe5, 0x67}

// CheckNetwork runs CheckToken (right chain, code at the address, matching
// EIP-712 domain) and confirms the token reports 6 decimals.
func CheckNetwork(ctx context.Context, r Reader, n Network) error {
	if err := CheckToken(ctx, r, n.TokenParams()); err != nil {
		return fmt.Errorf("%s: %w", n.Key, err)
	}
	addr := n.USDC
	got, err := r.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: decimalsSelector}, nil)
	if err != nil {
		return fmt.Errorf("%s: calling decimals(): %w", n.Key, redactURL(err))
	}
	if len(got) != 32 {
		return fmt.Errorf("%s: decimals() returned %d bytes, want 32", n.Key, len(got))
	}
	if d := new(big.Int).SetBytes(got); d.Cmp(big.NewInt(int64(n.Decimals))) != 0 {
		return fmt.Errorf("%s: token reports %s decimals, want %d", n.Key, d, n.Decimals)
	}
	return nil
}
