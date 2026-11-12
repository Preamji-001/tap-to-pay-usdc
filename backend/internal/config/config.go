// Package config reads the server's settings from environment variables.
// Every value is validated at startup, so a bad setting stops the server
// instead of producing a wrong payment later.
package config

import (
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

// minAPIKeyLen keeps the merchant API key from being short or guessable.
const minAPIKeyLen = 32

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	// Networks are the enabled chains, in CHAINS order. The first is the
	// primary chain: until checkouts can choose a chain, every checkout is
	// created and settled there.
	Networks []chain.Network
	// The primary network's values, kept as flat fields for the
	// single-chain wiring. They always equal Networks[0].
	RPCURL          string
	ChainID         int64
	USDCAddress     common.Address
	USDCName        string
	USDCVersion     string
	MerchantAddress common.Address
	MerchantAPIKey  string
	FrontendBaseURL string
	CORSOrigin      string
	CheckoutTTL     time.Duration
	// MaxAmountBaseUnits caps a single checkout, in token base units.
	MaxAmountBaseUnits *big.Int
	// The relayer's encrypted keystore and the file holding its password.
	RelayerKeystorePath string
	RelayerPasswordFile string
	// Confirmations is how many blocks deep a receipt must be to count
	// (primary network).
	Confirmations uint64
}

// Load builds a Config from getenv (os.Getenv in production). It reports
// every invalid variable at once, each error naming its variable.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	fail := func(key, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{key}, args...)...))
	}
	required := func(key string) string {
		v := getenv(key)
		if v == "" {
			fail(key, "is required")
		}
		return v
	}
	withDefault := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	address := func(key string) common.Address {
		v := required(key)
		if v == "" {
			return common.Address{}
		}
		if !common.IsHexAddress(v) {
			fail(key, "%q is not a 20-byte hex address", v)
			return common.Address{}
		}
		a := common.HexToAddress(v)
		if a == (common.Address{}) {
			fail(key, "must not be the zero address")
		}
		return a
	}

	cfg := Config{
		HTTPAddr:        withDefault("HTTP_ADDR", ":8080"),
		DatabaseURL:     required("DATABASE_URL"),
		MerchantAddress: address("MERCHANT_ADDRESS"),
		MerchantAPIKey:  getenv("MERCHANT_API_KEY"),
		FrontendBaseURL: required("FRONTEND_BASE_URL"),
		CORSOrigin:      required("CORS_ORIGIN"),

		RelayerKeystorePath: required("RELAYER_KEYSTORE_PATH"),
		RelayerPasswordFile: required("RELAYER_PASSWORD_FILE"),
	}

	// Browsers send Origin as scheme://host[:port] with no path; anything else
	// would never match and would silently break CORS.
	if cfg.CORSOrigin != "" {
		u, err := url.Parse(cfg.CORSOrigin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			fail("CORS_ORIGIN", "%q must be scheme://host[:port] with no trailing slash or path", cfg.CORSOrigin)
		}
	}

	if len(cfg.MerchantAPIKey) < minAPIKeyLen {
		fail("MERCHANT_API_KEY", "must be at least %d characters", minAPIKeyLen)
	}

	ttl, err := time.ParseDuration(withDefault("CHECKOUT_TTL", "15m"))
	if err != nil || ttl <= 0 {
		fail("CHECKOUT_TTL", "must be a positive duration like 15m")
	}
	cfg.CheckoutTTL = ttl

	maxAmount, ok := new(big.Int).SetString(withDefault("MAX_AMOUNT_BASE_UNITS", "1000000000"), 10)
	if !ok || maxAmount.Sign() <= 0 {
		fail("MAX_AMOUNT_BASE_UNITS", "must be a positive integer")
	}
	cfg.MaxAmountBaseUnits = maxAmount

	cfg.Networks = loadNetworks(getenv, fail)
	checkLegacySepolia(getenv, fail)
	if len(cfg.Networks) > 0 {
		p := cfg.Networks[0]
		cfg.RPCURL, cfg.ChainID, cfg.Confirmations = p.RPCURL, p.ChainID, p.Confirmations
		cfg.USDCAddress, cfg.USDCName, cfg.USDCVersion = p.USDC, p.EIP712Name, p.EIP712Version
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// envPrefix turns a network key into its env var prefix: "base-sepolia" → "BASE_SEPOLIA".
func envPrefix(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// loadNetworks reads CHAINS (default "sepolia") and, for each listed network,
// <PREFIX>_RPC_URL (required) and <PREFIX>_CONFIRMATIONS (optional). Sepolia
// keeps its original CONFIRMATIONS name. Token facts come from the built-in
// registry, never from env.
func loadNetworks(getenv func(string) string, fail func(key, format string, args ...any)) []chain.Network {
	list := getenv("CHAINS")
	if list == "" {
		list = "sepolia"
	}
	var nets []chain.Network
	seen := map[string]bool{}
	for _, key := range strings.Split(list, ",") {
		key = strings.TrimSpace(key)
		n, ok := chain.KnownNetwork(key)
		switch {
		case key == "":
			fail("CHAINS", "%q has an empty entry", list)
			continue
		case !ok:
			fail("CHAINS", "unknown network %q (known: %s)", key, strings.Join(chain.KnownNetworkKeys(), ", "))
			continue
		case seen[key]:
			fail("CHAINS", "%q is listed twice", key)
			continue
		}
		seen[key] = true

		prefix := envPrefix(key)
		rpcKey := prefix + "_RPC_URL"
		if n.RPCURL = getenv(rpcKey); n.RPCURL == "" {
			fail(rpcKey, "is required because CHAINS lists %s", key)
		}
		confKey := prefix + "_CONFIRMATIONS"
		if key == "sepolia" {
			confKey = "CONFIRMATIONS"
		}
		if v := getenv(confKey); v != "" {
			c, err := strconv.ParseUint(v, 10, 64)
			if err != nil || c < 1 {
				fail(confKey, "must be a positive integer")
			}
			n.Confirmations = c
		}
		nets = append(nets, n)
	}
	return nets
}

// checkLegacySepolia accepts the single-chain variables (CHAIN_ID,
// USDC_ADDRESS, USDC_EIP712_NAME, USDC_EIP712_VERSION) from older .env files.
// They are optional now, and when set they must match the registry's Sepolia
// entry, so a stale value fails loudly instead of being half-used.
func checkLegacySepolia(getenv func(string) string, fail func(key, format string, args ...any)) {
	s, _ := chain.KnownNetwork("sepolia")
	if v := getenv("CHAIN_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		switch {
		case err != nil || id <= 0:
			fail("CHAIN_ID", "%q is not a positive integer", v)
		case id != s.ChainID:
			fail("CHAIN_ID", "%d is not Sepolia (%d); use CHAINS to enable other networks", id, s.ChainID)
		}
	}
	if v := getenv("USDC_ADDRESS"); v != "" {
		switch {
		case !common.IsHexAddress(v):
			fail("USDC_ADDRESS", "%q is not a 20-byte hex address", v)
		case common.HexToAddress(v) != s.USDC:
			fail("USDC_ADDRESS", "%s differs from the registry's Sepolia USDC %s", v, s.USDC.Hex())
		}
	}
	if v := getenv("USDC_EIP712_NAME"); v != "" && v != s.EIP712Name {
		fail("USDC_EIP712_NAME", "%q differs from the registry's %q", v, s.EIP712Name)
	}
	if v := getenv("USDC_EIP712_VERSION"); v != "" && v != s.EIP712Version {
		fail("USDC_EIP712_VERSION", "%q differs from the registry's %q", v, s.EIP712Version)
	}
}
