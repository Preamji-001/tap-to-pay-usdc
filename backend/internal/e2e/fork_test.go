//go:build integration

// Package e2e runs the whole payment against the real USDC contracts on
// anvil forks of the testnets: create → sign → authorize → relay → verify.
// Several forks can run at once, one per chain, all served by one relayer key.
//
//	set -a; . ./.env; set +a
//	go test -tags integration ./internal/e2e/
//
// Sepolia forks from SEPOLIA_RPC_URL; Base Sepolia from BASE_SEPOLIA_RPC_URL,
// or its public RPC when that is unset.
package e2e

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/jackc/pgx/v5"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
	"github.com/Preamji-001/crypto-pos-base/internal/store"
)

var (
	merchant = common.HexToAddress("0x3CC845A0014aFcB0D52F673E0a98039074F1474c")
	// anvil's well-known dev keys #0 and #1, funded with ETH on every fork.
	relayerKey    = mustKey("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	frontRunerKey = mustKey("59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d")
)

var minterABI, _ = abi.JSON(strings.NewReader(`[
	{"type":"function","name":"masterMinter","stateMutability":"view","inputs":[],"outputs":[{"type":"address"}]},
	{"type":"function","name":"configureMinter","inputs":[{"type":"address"},{"type":"uint256"}],"outputs":[{"type":"bool"}]},
	{"type":"function","name":"mint","inputs":[{"type":"address"},{"type":"uint256"}],"outputs":[{"type":"bool"}]},
	{"type":"function","name":"balanceOf","stateMutability":"view","inputs":[{"type":"address"}],"outputs":[{"type":"uint256"}]}
]`))

func mustKey(hex string) *ecdsa.PrivateKey {
	k, err := crypto.HexToECDSA(hex)
	if err != nil {
		panic(err)
	}
	return k
}

// forkSource is the live RPC a testnet is forked from.
func forkSource(key string) string {
	switch key {
	case "sepolia":
		return os.Getenv("SEPOLIA_RPC_URL")
	case "base-sepolia":
		if u := os.Getenv("BASE_SEPOLIA_RPC_URL"); u != "" {
			return u
		}
		return "https://sepolia.base.org"
	}
	return ""
}

// fork is one anvil fork of a testnet.
type fork struct {
	net    chain.Network // RPCURL points at the fork
	rpc    *rpc.Client
	client *ethclient.Client
}

type env struct {
	forks map[string]*fork
	svc   *checkout.Service
	rec   *checkout.Reconciler
	repo  *store.Store
}

// startFork runs one anvil fork per named network and wires the real store,
// and one USDC reader and relayer per fork, into a single reconciler.
func startFork(t *testing.T, keys ...string) *env {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("needs TEST_DATABASE_URL")
	}
	if _, err := exec.LookPath("anvil"); err != nil {
		t.Skip("anvil not installed")
	}
	ctx := context.Background()

	e := &env{forks: map[string]*fork{}}
	chains := checkout.Chains{}
	var nets []chain.Network
	var lanes []checkout.Lane
	for _, key := range keys {
		src := forkSource(key)
		if src == "" {
			t.Skipf("needs an RPC URL to fork %s", key)
		}
		n, ok := chain.KnownNetwork(key)
		if !ok {
			t.Fatalf("unknown network %s", key)
		}
		f := startAnvil(t, src)
		n.RPCURL, n.Confirmations = f.url, 1
		f.net = n
		if err := chain.CheckNetwork(ctx, f.client, n); err != nil {
			t.Fatalf("fork of %s: %v", key, err)
		}
		e.forks[key] = &f.fork
		nets = append(nets, n)
		usdc := chain.NewUSDC(f.client, n.USDC)
		chains[n.ChainID] = usdc
		lanes = append(lanes, checkout.Lane{Network: n, Chain: usdc, Sender: chain.NewRelayer(f.client, relayerKey, n.USDC, n.ChainID)})
	}

	db, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// The reconciler walks every active row, so start from an empty table.
	// Run this package on its own: the store tests share the same database.
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, "TRUNCATE checkouts")
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}

	networks, err := chain.NewRegistry(nets)
	if err != nil {
		t.Fatal(err)
	}
	e.repo = db
	e.rec = checkout.NewReconciler(db, lanes, time.Now)
	e.svc = checkout.NewService(db, checkout.Params{
		Merchant: merchant, Networks: networks, TTL: 15 * time.Minute,
		MaxAmount: big.NewInt(1_000_000_000), FrontendBaseURL: "http://localhost:5173",
	}, time.Now, chains, e.rec)
	return e
}

type startedFork struct {
	fork
	url string
}

func startAnvil(t *testing.T, forkURL string) *startedFork {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	anvil := exec.Command("anvil", "--fork-url", forkURL, "--port", itoa(port), "--silent")
	if err := anvil.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { anvil.Process.Kill(); anvil.Wait() })

	ctx := context.Background()
	url := "http://127.0.0.1:" + itoa(port)
	var rc *rpc.Client
	for i := 0; ; i++ {
		rc, err = rpc.DialContext(ctx, url)
		if err == nil {
			var id hexutil.Big
			if err = rc.CallContext(ctx, &id, "eth_chainId"); err == nil {
				break
			}
		}
		if i == 60 {
			t.Fatalf("anvil did not start: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return &startedFork{fork: fork{rpc: rc, client: ethclient.NewClient(rc)}, url: url}
}

func itoa(i int) string { return big.NewInt(int64(i)).String() }

// fund mints fork USDC to account by impersonating the token's masterMinter.
func (f *fork) fund(t *testing.T, account common.Address, amount int64) {
	t.Helper()
	ctx := context.Background()
	usdc := f.net.USDC
	out, err := f.client.CallContract(ctx, ethereum.CallMsg{To: &usdc, Data: pack(t, "masterMinter")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mm := common.BytesToAddress(out)
	must(t, f.rpc.CallContext(ctx, nil, "anvil_impersonateAccount", mm))
	must(t, f.rpc.CallContext(ctx, nil, "anvil_setBalance", mm, "0xde0b6b3a7640000"))
	f.sendAs(t, mm, pack(t, "configureMinter", mm, big.NewInt(1_000_000_000_000)))
	f.sendAs(t, mm, pack(t, "mint", account, big.NewInt(amount)))
}

func (f *fork) sendAs(t *testing.T, from common.Address, data []byte) {
	t.Helper()
	var hash common.Hash
	must(t, f.rpc.CallContext(context.Background(), &hash, "eth_sendTransaction", map[string]any{
		"from": from, "to": f.net.USDC, "data": hexutil.Bytes(data),
	}))
	if r := f.waitReceipt(t, hash); r.Status != 1 {
		t.Fatalf("impersonated tx reverted: %+v", r)
	}
}

// waitReceipt waits for anvil to mine hash (automine is near-instant, not synchronous).
func (f *fork) waitReceipt(t *testing.T, hash common.Hash) *types.Receipt {
	t.Helper()
	for range 100 {
		if r, err := f.client.TransactionReceipt(context.Background(), hash); err == nil {
			return r
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("tx %s never mined on %s", hash, f.net.Key)
	return nil
}

func (f *fork) balance(t *testing.T, a common.Address) *big.Int {
	t.Helper()
	usdc := f.net.USDC
	out, err := f.client.CallContract(context.Background(), ethereum.CallMsg{To: &usdc, Data: pack(t, "balanceOf", a)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return new(big.Int).SetBytes(out)
}

func pack(t *testing.T, method string, args ...any) []byte {
	t.Helper()
	b, err := minterABI.Pack(method, args...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// authorize creates a checkout (no chain yet) and has payer sign it for key's
// chain and submit it, which binds it there.
func (e *env) authorize(t *testing.T, key string, payer *ecdsa.PrivateKey, amount string) (*checkout.Checkout, []byte) {
	t.Helper()
	c, err := e.svc.Create(context.Background(), checkout.CreateRequest{Amount: amount})
	if err != nil {
		t.Fatal(err)
	}
	if c.Bound() {
		t.Fatalf("new checkout already bound to %d", c.ChainID)
	}
	return c, e.payOn(t, c, key, payer)
}

// payOn signs c for key's chain, the way the page would after GET ?chain=,
// and submits it with that chain_id.
func (e *env) payOn(t *testing.T, c *checkout.Checkout, key string, payer *ecdsa.PrivateKey) []byte {
	t.Helper()
	chainID := e.forks[key].net.ChainID
	from := crypto.PubkeyToAddress(payer.PublicKey)
	domain, err := e.svc.DomainSeparator(c, chainID)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.Sign(c.Authorization(from).Digest(domain).Bytes(), payer)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	got, err := e.svc.Authorize(context.Background(), c.ID, chainID, from, sig)
	if err != nil {
		t.Fatalf("Authorize on %s: %v", key, err)
	}
	if got.ChainID != chainID {
		t.Fatalf("bound to %d, want %s", got.ChainID, key)
	}
	return sig
}

func (e *env) stepUntil(t *testing.T, id string, want checkout.Status) *checkout.Checkout {
	t.Helper()
	for range 50 {
		e.rec.Step(context.Background())
		c, err := e.repo.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if c.Status == want {
			return c
		}
		if c.Status.Terminal() {
			t.Fatalf("status %s (%q), want %s", c.Status, c.FailureReason, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("never reached %s", want)
	return nil
}

func TestFork_PaymentConfirmed(t *testing.T) {
	e := startFork(t, "sepolia")
	sep := e.forks["sepolia"]
	payer, _ := crypto.GenerateKey()
	sep.fund(t, crypto.PubkeyToAddress(payer.PublicKey), 5_000_000)
	before := sep.balance(t, merchant)

	c, _ := e.authorize(t, "sepolia", payer, "1.5")
	got := e.stepUntil(t, c.ID, checkout.StatusConfirmed)

	if diff := new(big.Int).Sub(sep.balance(t, merchant), before); diff.Int64() != 1_500_000 {
		t.Fatalf("merchant received %s base units, want 1500000", diff)
	}
	if r := sep.waitReceipt(t, *got.TxHash); r.Status != 1 {
		t.Fatalf("receipt %+v", r)
	}
	if bal := sep.balance(t, crypto.PubkeyToAddress(payer.PublicKey)); bal.Int64() != 3_500_000 {
		t.Fatalf("payer has %s left, want 3500000", bal)
	}
}

func TestFork_FrontRun_ConfirmedWithTheirHash(t *testing.T) {
	e := startFork(t, "sepolia")
	sep := e.forks["sepolia"]
	payer, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(payer.PublicKey)
	sep.fund(t, from, 5_000_000)

	c, sig := e.authorize(t, "sepolia", payer, "2")
	// Someone else holding the signature sends it before our relayer does.
	theirs, _, err := chain.NewRelayer(sep.client, frontRunerKey, sep.net.USDC, sep.net.ChainID).
		Send(context.Background(), c.Authorization(from), sig, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := e.stepUntil(t, c.ID, checkout.StatusConfirmed)
	if *got.TxHash != theirs {
		t.Fatalf("tx hash %s, want the front-runner's %s", got.TxHash, theirs)
	}
}

// L3: one server, one relayer key, two chains. Each checkout is relayed and
// verified on its own chain, and only that chain's USDC moves.
func TestFork_BaseAndSepoliaSettleOnTheirOwnChains(t *testing.T) {
	e := startFork(t, "sepolia", "base-sepolia")
	sep, base := e.forks["sepolia"], e.forks["base-sepolia"]
	payer, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(payer.PublicKey)
	sep.fund(t, from, 5_000_000)
	base.fund(t, from, 5_000_000)
	sepBefore, baseBefore := sep.balance(t, merchant), base.balance(t, merchant)

	onSep, _ := e.authorize(t, "sepolia", payer, "1.5")
	onBase, _ := e.authorize(t, "base-sepolia", payer, "2.25")
	gotSep := e.stepUntil(t, onSep.ID, checkout.StatusConfirmed)
	gotBase := e.stepUntil(t, onBase.ID, checkout.StatusConfirmed)

	if d := new(big.Int).Sub(sep.balance(t, merchant), sepBefore); d.Int64() != 1_500_000 {
		t.Errorf("Sepolia merchant received %s, want 1500000", d)
	}
	if d := new(big.Int).Sub(base.balance(t, merchant), baseBefore); d.Int64() != 2_250_000 {
		t.Errorf("Base merchant received %s, want 2250000", d)
	}
	// Each payment's transaction exists only on its own chain.
	if r := base.waitReceipt(t, *gotBase.TxHash); r.Status != 1 {
		t.Errorf("Base receipt %+v", r)
	}
	if _, err := sep.client.TransactionReceipt(context.Background(), *gotBase.TxHash); !errors.Is(err, ethereum.NotFound) {
		t.Errorf("Base payment's tx found on Sepolia (err %v)", err)
	}
	if _, err := base.client.TransactionReceipt(context.Background(), *gotSep.TxHash); !errors.Is(err, ethereum.NotFound) {
		t.Errorf("Sepolia payment's tx found on Base (err %v)", err)
	}
}

// L4: the customer's wallet is on Sepolia, but their USDC is on Base. Routes
// reads both chains for real and recommends Base; the payment is signed for
// Base, bound there when authorized, and settled there.
func TestFork_LateBinding_PaysWhereTheMoneyIs(t *testing.T) {
	e := startFork(t, "sepolia", "base-sepolia")
	sep, base := e.forks["sepolia"], e.forks["base-sepolia"]
	payer, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(payer.PublicKey)
	base.fund(t, from, 5_000_000)
	baseBefore := base.balance(t, merchant)

	ctx := context.Background()
	c, err := e.svc.Create(ctx, checkout.CreateRequest{Amount: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.StartBlocks) != 2 {
		t.Fatalf("start blocks %v, want one per fork", c.StartBlocks)
	}
	routes, err := e.svc.Routes(ctx, c.ID, from, sep.net.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	if routes.Recommended != base.net.ChainID {
		t.Fatalf("recommended %d, want Base (the only chain with USDC): %+v", routes.Recommended, routes.Routes)
	}
	for _, r := range routes.Routes {
		if r.Balance == nil {
			t.Fatalf("%s balance unknown", r.Network.Key)
		}
	}

	// Paying on Sepolia, where the wallet is, fails the pre-flight and binds nothing.
	sepDomain, _ := e.svc.DomainSeparator(c, sep.net.ChainID)
	sig, _ := crypto.Sign(c.Authorization(from).Digest(sepDomain).Bytes(), payer)
	sig[64] += 27
	if _, err := e.svc.Authorize(ctx, c.ID, sep.net.ChainID, from, sig); !errors.Is(err, checkout.ErrInsufficientBalance) {
		t.Fatalf("Sepolia attempt: %v, want ErrInsufficientBalance", err)
	}

	e.payOn(t, c, "base-sepolia", payer)
	got := e.stepUntil(t, c.ID, checkout.StatusConfirmed)
	if got.ChainID != base.net.ChainID || got.CreatedBlock != c.StartBlocks[base.net.ChainID] {
		t.Fatalf("confirmed on chain %d from block %d", got.ChainID, got.CreatedBlock)
	}
	if d := new(big.Int).Sub(base.balance(t, merchant), baseBefore); d.Int64() != 4_000_000 {
		t.Fatalf("Base merchant received %s, want 4000000", d)
	}
}
