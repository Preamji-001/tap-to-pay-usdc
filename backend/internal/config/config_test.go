package config

import (
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":        "postgres://pos:pos@localhost:5433/pos?sslmode=disable",
		"SEPOLIA_RPC_URL":     "https://rpc.example",
		"CHAIN_ID":            "11155111",
		"USDC_ADDRESS":        "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"USDC_EIP712_NAME":    "USDC",
		"USDC_EIP712_VERSION": "2",
		"MERCHANT_ADDRESS":    "0x3CC845A0014aFcB0D52F673E0a98039074F1474c",
		"MERCHANT_API_KEY":    strings.Repeat("k", 32),
		"FRONTEND_BASE_URL":   "http://localhost:5173",
		"CORS_ORIGIN":         "http://localhost:5173",

		"RELAYER_KEYSTORE_PATH": "/keys/relayer.json",
		"RELAYER_PASSWORD_FILE": "/keys/relayer.password",
	}
}

func getenvFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(getenvFrom(validEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ChainID != 11155111 {
		t.Errorf("ChainID = %d", cfg.ChainID)
	}
	if cfg.CheckoutTTL != 15*time.Minute {
		t.Errorf("CheckoutTTL = %v, want 15m default", cfg.CheckoutTTL)
	}
	if got := cfg.MaxAmountBaseUnits.String(); got != "1000000000" {
		t.Errorf("MaxAmountBaseUnits = %s, want 1000000000 default", got)
	}
	if cfg.Confirmations != 1 {
		t.Errorf("Confirmations = %d, want 1 default", cfg.Confirmations)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080 default", cfg.HTTPAddr)
	}
	if cfg.USDCAddress.Hex() != "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" {
		t.Errorf("USDCAddress = %s", cfg.USDCAddress.Hex())
	}
	if cfg.MerchantAddress.Hex() != "0x3CC845A0014aFcB0D52F673E0a98039074F1474c" {
		t.Errorf("MerchantAddress = %s", cfg.MerchantAddress.Hex())
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{"missing database url", "DATABASE_URL", ""},
		{"missing rpc url", "SEPOLIA_RPC_URL", ""},
		{"short usdc address", "USDC_ADDRESS", "0x123"},
		{"zero merchant", "MERCHANT_ADDRESS", "0x0000000000000000000000000000000000000000"},
		{"empty api key", "MERCHANT_API_KEY", ""},
		{"31 char api key", "MERCHANT_API_KEY", strings.Repeat("k", 31)},
		{"non-numeric chain id", "CHAIN_ID", "abc"},
		{"zero ttl", "CHECKOUT_TTL", "0s"},
		{"negative max amount", "MAX_AMOUNT_BASE_UNITS", "-5"},
		{"cors origin with trailing slash", "CORS_ORIGIN", "http://localhost:5173/"},
		{"cors origin with path", "CORS_ORIGIN", "http://localhost:5173/app"},
		{"cors origin without scheme", "CORS_ORIGIN", "localhost:5173"},
		{"missing relayer keystore", "RELAYER_KEYSTORE_PATH", ""},
		{"missing relayer password file", "RELAYER_PASSWORD_FILE", ""},
		{"zero confirmations", "CONFIRMATIONS", "0"},
		{"non-numeric confirmations", "CONFIRMATIONS", "two"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := Load(getenvFrom(env))
			if err == nil {
				t.Fatalf("Load succeeded, want error for %s=%q", tt.key, tt.value)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q does not name %s", err, tt.key)
			}
		})
	}
}

func TestLoadDefaultsToSepoliaOnly(t *testing.T) {
	cfg, err := Load(getenvFrom(validEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Networks) != 1 || cfg.Networks[0].Key != "sepolia" {
		t.Fatalf("Networks = %+v, want only sepolia", cfg.Networks)
	}
	if n := cfg.Networks[0]; n.RPCURL != "https://rpc.example" || n.Confirmations != 1 {
		t.Errorf("sepolia = %+v", n)
	}
}

func TestLoadThreeChains(t *testing.T) {
	env := validEnv()
	env["CHAINS"] = "sepolia, base-sepolia,amoy"
	env["BASE_SEPOLIA_RPC_URL"] = "https://base.example"
	env["AMOY_RPC_URL"] = "https://amoy.example"
	env["AMOY_CONFIRMATIONS"] = "5"
	env["CONFIRMATIONS"] = "2"
	cfg, err := Load(getenvFrom(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []struct {
		key           string
		id            int64
		rpc           string
		confirmations uint64
	}{
		{"sepolia", 11155111, "https://rpc.example", 2},
		{"base-sepolia", 84532, "https://base.example", 1},
		{"amoy", 80002, "https://amoy.example", 5},
	}
	if len(cfg.Networks) != len(want) {
		t.Fatalf("got %d networks", len(cfg.Networks))
	}
	for i, w := range want {
		n := cfg.Networks[i]
		if n.Key != w.key || n.ChainID != w.id || n.RPCURL != w.rpc || n.Confirmations != w.confirmations {
			t.Errorf("Networks[%d] = %+v, want %+v", i, n, w)
		}
	}
	if cfg.ChainID != 11155111 || cfg.Confirmations != 2 {
		t.Errorf("primary fields = %d / %d, want sepolia's", cfg.ChainID, cfg.Confirmations)
	}
}

func TestLoadWithoutSepolia(t *testing.T) {
	env := validEnv()
	for _, k := range []string{"SEPOLIA_RPC_URL", "CHAIN_ID", "USDC_ADDRESS", "USDC_EIP712_NAME", "USDC_EIP712_VERSION"} {
		delete(env, k)
	}
	env["CHAINS"] = "base-sepolia"
	env["BASE_SEPOLIA_RPC_URL"] = "https://base.example"
	cfg, err := Load(getenvFrom(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ChainID != 84532 || cfg.USDCAddress.Hex() != "0x036CbD53842c5426634e7929541eC2318f3dCF7e" || cfg.RPCURL != "https://base.example" {
		t.Errorf("primary = %d %s %s", cfg.ChainID, cfg.USDCAddress.Hex(), cfg.RPCURL)
	}
}

func TestLoadRejectsNetworks(t *testing.T) {
	tests := []struct {
		name    string
		set     map[string]string
		wantKey string
	}{
		{"unknown chain", map[string]string{"CHAINS": "sepolia,mainnet"}, "CHAINS"},
		{"duplicate chain", map[string]string{"CHAINS": "sepolia,sepolia"}, "CHAINS"},
		{"empty entry", map[string]string{"CHAINS": "sepolia,,amoy", "AMOY_RPC_URL": "https://a"}, "CHAINS"},
		{"missing base rpc", map[string]string{"CHAINS": "sepolia,base-sepolia"}, "BASE_SEPOLIA_RPC_URL"},
		{"zero amoy confirmations", map[string]string{"CHAINS": "amoy", "AMOY_RPC_URL": "https://a", "AMOY_CONFIRMATIONS": "0"}, "AMOY_CONFIRMATIONS"},
		{"legacy chain id not sepolia", map[string]string{"CHAIN_ID": "84532"}, "CHAIN_ID"},
		{"legacy usdc differs", map[string]string{"USDC_ADDRESS": "0x036CbD53842c5426634e7929541eC2318f3dCF7e"}, "USDC_ADDRESS"},
		{"legacy name differs", map[string]string{"USDC_EIP712_NAME": "USD Coin"}, "USDC_EIP712_NAME"},
		{"legacy version differs", map[string]string{"USDC_EIP712_VERSION": "1"}, "USDC_EIP712_VERSION"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			for k, v := range tt.set {
				env[k] = v
			}
			_, err := Load(getenvFrom(env))
			if err == nil || !strings.Contains(err.Error(), tt.wantKey) {
				t.Fatalf("Load = %v, want error naming %s", err, tt.wantKey)
			}
		})
	}
}
