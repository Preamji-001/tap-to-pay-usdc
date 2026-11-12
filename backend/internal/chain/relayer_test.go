package chain

import (
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

// sendClient is a Client for the relayer's send path.
type sendClient struct {
	Client
	estimateErr error
	pending     uint64
	sent        []*types.Transaction
	logs        []types.Log
	head        uint64
	queries     []ethereum.FilterQuery
}

func (c *sendClient) EstimateGas(context.Context, ethereum.CallMsg) (uint64, error) {
	return 50_000, c.estimateErr
}
func (c *sendClient) SuggestGasTipCap(context.Context) (*big.Int, error) { return big.NewInt(2), nil }
func (c *sendClient) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return &types.Header{BaseFee: big.NewInt(10)}, nil
}
func (c *sendClient) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	return c.pending, nil
}
func (c *sendClient) SendTransaction(_ context.Context, tx *types.Transaction) error {
	c.sent = append(c.sent, tx)
	return nil
}
func (c *sendClient) BlockNumber(context.Context) (uint64, error) { return c.head, nil }
func (c *sendClient) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	c.queries = append(c.queries, q)
	var out []types.Log
	for _, l := range c.logs {
		if l.BlockNumber >= q.FromBlock.Uint64() && l.BlockNumber <= q.ToBlock.Uint64() {
			out = append(out, l)
		}
	}
	return out, nil
}

func TestRelayer_SendBuildsSignedEIP1559Tx(t *testing.T) {
	key := newKey(t)
	c := &sendClient{pending: 7}
	r := NewRelayer(c, key, sepoliaUSDC, 11155111)
	a := testAuthorization(common.HexToAddress("0xaa"))
	sig := sign(t, newKey(t), a.Digest(sepoliaUSDCDomain))

	hash, nonce, err := r.Send(context.Background(), a, sig, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx := c.sent[0]
	from, err := types.Sender(types.LatestSignerForChainID(big.NewInt(11155111)), tx)
	if err != nil || from != crypto.PubkeyToAddress(key.PublicKey) {
		t.Fatalf("signed by %s (%v)", from, err)
	}
	want, _ := packTransfer(a, sig)
	switch {
	case hash != tx.Hash() || nonce != 7 || tx.Nonce() != 7:
		t.Errorf("hash/nonce = %s/%d, tx %s/%d", hash, nonce, tx.Hash(), tx.Nonce())
	case *tx.To() != sepoliaUSDC || string(tx.Data()) != string(want):
		t.Errorf("tx goes to %s with other data", tx.To())
	case tx.Gas() != 60_000:
		t.Errorf("gas = %d, want estimate + 20%%", tx.Gas())
	case tx.GasTipCap().Int64() != 2 || tx.GasFeeCap().Int64() != 22:
		t.Errorf("fees tip %s cap %s, want 2 and 2*10+2", tx.GasTipCap(), tx.GasFeeCap())
	case tx.ChainId().Int64() != 11155111 || tx.Value().Sign() != 0:
		t.Errorf("chain %s value %s", tx.ChainId(), tx.Value())
	}
}

func TestRelayer_ReusesNonce(t *testing.T) {
	c := &sendClient{pending: 9}
	r := NewRelayer(c, newKey(t), sepoliaUSDC, 11155111)
	reuse := uint64(4)
	_, nonce, err := r.Send(context.Background(), testAuthorization(common.Address{}), make([]byte, 65), &reuse)
	if err != nil || nonce != 4 || c.sent[0].Nonce() != 4 {
		t.Fatalf("nonce %d (%v), want the reused 4", nonce, err)
	}
}

func TestRelayer_EstimateRevertIsRevertError(t *testing.T) {
	c := &sendClient{estimateErr: revertErr{msg: "execution reverted: FiatTokenV2: authorization is used or canceled"}}
	_, _, err := NewRelayer(c, newKey(t), sepoliaUSDC, 11155111).Send(context.Background(), testAuthorization(common.Address{}), make([]byte, 65), nil)
	var re *RevertError
	if !errors.As(err, &re) || re.Reason != "FiatTokenV2: authorization is used or canceled" {
		t.Fatalf("err = %v", err)
	}
	if len(c.sent) != 0 {
		t.Fatal("sent a transaction that would revert")
	}
}

func TestLoadRelayerKey(t *testing.T) {
	key := newKey(t)
	dir := t.TempDir()
	keyJSON, err := keystore.EncryptKey(&keystore.Key{Id: uuid.New(), Address: crypto.PubkeyToAddress(key.PublicKey), PrivateKey: key},
		"hunter2", keystore.LightScryptN, keystore.LightScryptP)
	if err != nil {
		t.Fatal(err)
	}
	ks, pw := filepath.Join(dir, "relayer.json"), filepath.Join(dir, "password")
	os.WriteFile(ks, keyJSON, 0o600)
	os.WriteFile(pw, []byte("hunter2\n"), 0o600)

	got, err := LoadRelayerKey(ks, pw)
	if err != nil || !got.Equal(key) {
		t.Fatalf("LoadRelayerKey = %v", err)
	}
	os.WriteFile(pw, []byte("wrong"), 0o600)
	if _, err := LoadRelayerKey(ks, pw); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func authLog(payer common.Address, nonce [32]byte, block uint64, tx common.Hash) types.Log {
	return types.Log{
		Address:     sepoliaUSDC,
		Topics:      []common.Hash{authorizationUsedTopic, common.BytesToHash(payer.Bytes()), common.Hash(nonce)},
		BlockNumber: block,
		TxHash:      tx,
	}
}

func TestFindAuthorizationUsed_ChunksAndFilters(t *testing.T) {
	payer := common.HexToAddress("0xaa")
	var nonce [32]byte
	nonce[3] = 9
	c := &sendClient{head: 1_300, logs: []types.Log{authLog(payer, nonce, 1_250, common.HexToHash("0x77"))}}
	u := NewUSDC(c, sepoliaUSDC)

	got, err := u.FindAuthorizationUsed(context.Background(), &payer, nonce, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Payer != payer || got.TxHash != common.HexToHash("0x77") || got.Block != 1_250 {
		t.Fatalf("got %+v", got)
	}
	if len(c.queries) != 3 {
		t.Fatalf("%d queries for blocks 100..1300, want 3 chunks of ≤500", len(c.queries))
	}
	q := c.queries[0]
	if q.Addresses[0] != sepoliaUSDC || q.Topics[0][0] != authorizationUsedTopic ||
		q.Topics[1][0] != common.BytesToHash(payer.Bytes()) || q.Topics[2][0] != common.Hash(nonce) {
		t.Errorf("query = %+v", q)
	}

	c.queries = nil
	if _, err := u.FindAuthorizationUsed(context.Background(), nil, nonce, 1_000); err != nil || c.queries[0].Topics[1] != nil {
		t.Errorf("without payer the payer topic must be a wildcard: %+v (%v)", c.queries[0].Topics, err)
	}
	if got, _ := u.FindAuthorizationUsed(context.Background(), &payer, nonce, 1_260); got != nil {
		t.Errorf("found a log before fromBlock: %+v", got)
	}
}

func transferLog(token, from, to common.Address, amount int64) *types.Log {
	return &types.Log{
		Address: token,
		Topics:  []common.Hash{transferTopic, common.BytesToHash(from.Bytes()), common.BytesToHash(to.Bytes())},
		Data:    common.LeftPadBytes(big.NewInt(amount).Bytes(), 32),
	}
}

func TestVerifyTransfer(t *testing.T) {
	payer, merchant := common.HexToAddress("0xaa"), common.HexToAddress("0xbb")
	var nonce [32]byte
	nonce[0] = 1
	want := ExpectedTransfer{Token: sepoliaUSDC, Payer: payer, Merchant: merchant, Amount: big.NewInt(100), Nonce: nonce}
	auth := authLog(payer, nonce, 1, common.Hash{})
	good := func() *types.Receipt {
		a := auth
		return &types.Receipt{Status: 1, Logs: []*types.Log{&a, transferLog(sepoliaUSDC, payer, merchant, 100)}}
	}
	if err := VerifyTransfer(good(), want); err != nil {
		t.Fatalf("good receipt: %v", err)
	}

	otherNonce := nonce
	otherNonce[0] = 2
	for name, change := range map[string]func(r *types.Receipt){
		"reverted":         func(r *types.Receipt) { r.Status = 0 },
		"wrong amount":     func(r *types.Receipt) { r.Logs[1] = transferLog(sepoliaUSDC, payer, merchant, 99) },
		"wrong recipient":  func(r *types.Receipt) { r.Logs[1] = transferLog(sepoliaUSDC, payer, payer, 100) },
		"wrong sender":     func(r *types.Receipt) { r.Logs[1] = transferLog(sepoliaUSDC, merchant, merchant, 100) },
		"other token":      func(r *types.Receipt) { r.Logs[1] = transferLog(common.HexToAddress("0xdead"), payer, merchant, 100) },
		"no transfer":      func(r *types.Receipt) { r.Logs = r.Logs[:1] },
		"no authorization": func(r *types.Receipt) { r.Logs = r.Logs[1:] },
		"other nonce":      func(r *types.Receipt) { l := authLog(payer, otherNonce, 1, common.Hash{}); r.Logs[0] = &l },
		"fake auth log":    func(r *types.Receipt) { r.Logs[0].Address = common.HexToAddress("0xdead") },
	} {
		r := good()
		change(r)
		if err := VerifyTransfer(r, want); !errors.Is(err, ErrVerification) {
			t.Errorf("%s: err = %v, want ErrVerification", name, err)
		}
	}
}
