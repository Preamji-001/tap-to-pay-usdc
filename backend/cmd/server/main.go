// Command server runs the checkout API.
//
//	set -a; . ./.env; set +a
//	go run ./cmd/server
package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
	"github.com/Preamji-001/crypto-pos-base/internal/config"
	"github.com/Preamji-001/crypto-pos-base/internal/httpapi"
	"github.com/Preamji-001/crypto-pos-base/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// 0.01 of the gas token (ETH or POL, both 18 decimals): enough for dozens of
// transferWithAuthorization sends on a testnet.
var minRelayerBalance = big.NewInt(10_000_000_000_000_000)

func weiToEth(wei *big.Int) string {
	return new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18)).Text('f', 4)
}

// dialAndCheck connects to n's RPC and confirms its USDC matches the registry.
func dialAndCheck(ctx context.Context, n chain.Network) (*ethclient.Client, error) {
	c, err := ethclient.DialContext(ctx, n.RPCURL)
	if err != nil {
		// The URL may carry an API key, so it is never logged.
		return nil, fmt.Errorf("connecting to the %s RPC failed", n.Key)
	}
	if err := chain.CheckNetwork(ctx, c, n); err != nil {
		c.Close()
		return nil, fmt.Errorf("token check: %w", err)
	}
	slog.Info("token check passed", "network", n.Key, "chain_id", n.ChainID, "token", n.USDC.Hex())
	return c, nil
}

// checkRelayerBalance logs the relayer's gas balance on n and warns when it is
// low. A low balance is only a warning; failing to read it stops startup.
func checkRelayerBalance(ctx context.Context, c *ethclient.Client, n chain.Network, relayer common.Address) error {
	balance, err := c.BalanceAt(ctx, relayer, nil)
	if err != nil {
		return fmt.Errorf("reading the relayer's %s balance on %s failed", n.GasToken, n.Key)
	}
	slog.Info("relayer balance", "network", n.Key, strings.ToLower(n.GasToken), weiToEth(balance))
	if balance.Cmp(minRelayerBalance) < 0 {
		slog.Warn("relayer balance is below 0.01; fund it or sends will fail",
			"network", n.Key, "gas_token", n.GasToken, "address", relayer.Hex())
	}
	return nil
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config:\n%w", err)
	}

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	registry, err := chain.NewRegistry(cfg.Networks)
	if err != nil {
		return fmt.Errorf("networks: %w", err)
	}
	relayerKey, err := chain.LoadRelayerKey(cfg.RelayerKeystorePath, cfg.RelayerPasswordFile)
	if err != nil {
		return err
	}

	// Dial and check every enabled network: a wrong token on any of them
	// stops startup. Each network gets its own USDC reader and its own
	// relayer. The key (and so the address) is the same everywhere, but every
	// chain has its own account nonce, gas balance and send lock.
	relayerAddr := crypto.PubkeyToAddress(relayerKey.PublicKey)
	clients := make(map[int64]*ethclient.Client)
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	chains := checkout.Chains{}
	var lanes []checkout.Lane
	for _, n := range registry.Networks() {
		c, err := dialAndCheck(startCtx, n)
		if err != nil {
			return err
		}
		clients[n.ChainID] = c
		if err := checkRelayerBalance(startCtx, c, n, relayerAddr); err != nil {
			return err
		}
		usdc := chain.NewUSDC(c, n.USDC)
		chains[n.ChainID] = usdc
		lanes = append(lanes, checkout.Lane{Network: n, Chain: usdc, Sender: chain.NewRelayer(c, relayerKey, n.USDC, n.ChainID)})
	}
	slog.Info("relayer ready", "address", relayerAddr.Hex(), "networks", len(lanes), "primary", registry.Primary().Key)

	db, err := store.Open(startCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(startCtx); err != nil {
		return err
	}

	reconciler := checkout.NewReconciler(db, lanes, time.Now)
	svc := checkout.NewService(db, checkout.Params{
		Merchant:        cfg.MerchantAddress,
		Networks:        registry,
		TTL:             cfg.CheckoutTTL,
		MaxAmount:       cfg.MaxAmountBaseUnits,
		FrontendBaseURL: cfg.FrontendBaseURL,
	}, time.Now, chains, reconciler)
	go reconciler.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewServer(svc, cfg.MerchantAPIKey, cfg.CORSOrigin),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.HTTPAddr, "merchant", cfg.MerchantAddress.Hex())
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	return srv.Shutdown(shutdownCtx)
}
