package chain

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// LoadRelayerKey decrypts the relayer's keystore file with the password in
// passwordFile. Neither the key nor the password is ever logged.
func LoadRelayerKey(keystorePath, passwordFile string) (*ecdsa.PrivateKey, error) {
	keyJSON, err := os.ReadFile(keystorePath)
	if err != nil {
		return nil, fmt.Errorf("reading relayer keystore: %w", err)
	}
	password, err := os.ReadFile(passwordFile)
	if err != nil {
		return nil, fmt.Errorf("reading relayer password file: %w", err)
	}
	key, err := keystore.DecryptKey(keyJSON, strings.TrimRight(string(password), "\r\n"))
	if err != nil {
		return nil, fmt.Errorf("decrypting relayer keystore: %w", err)
	}
	return key.PrivateKey, nil
}

// Relayer sends transferWithAuthorization from its own account, paying the
// gas. It only ever calls the configured token.
type Relayer struct {
	c       Client
	key     *ecdsa.PrivateKey
	from    common.Address
	token   common.Address
	chainID *big.Int
	mu      sync.Mutex // one send at a time, so account nonces never collide
}

func NewRelayer(c Client, key *ecdsa.PrivateKey, token common.Address, chainID int64) *Relayer {
	return &Relayer{c: c, key: key, from: crypto.PubkeyToAddress(key.PublicKey), token: token, chainID: big.NewInt(chainID)}
}

func (r *Relayer) Address() common.Address { return r.from }

// Send submits the authorization. With reuseNonce it replaces a dropped
// transaction at the same account nonce; otherwise it takes the next one.
// A revert while estimating gas comes back as *RevertError and costs nothing.
func (r *Relayer) Send(ctx context.Context, a TransferAuthorization, sig []byte, reuseNonce *uint64) (common.Hash, uint64, error) {
	data, err := packTransfer(a, sig)
	if err != nil {
		return common.Hash{}, 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	gas, err := r.c.EstimateGas(ctx, ethereum.CallMsg{From: r.from, To: &r.token, Data: data})
	if err != nil {
		return common.Hash{}, 0, fmt.Errorf("estimating gas: %w", classifyCallError(err))
	}
	tip, err := r.c.SuggestGasTipCap(ctx)
	if err != nil {
		return common.Hash{}, 0, fmt.Errorf("reading tip: %w", redactURL(err))
	}
	head, err := r.c.HeaderByNumber(ctx, nil)
	if err != nil {
		return common.Hash{}, 0, fmt.Errorf("reading head: %w", redactURL(err))
	}
	var nonce uint64
	if reuseNonce != nil {
		nonce = *reuseNonce
	} else if nonce, err = r.c.PendingNonceAt(ctx, r.from); err != nil {
		return common.Hash{}, 0, fmt.Errorf("reading account nonce: %w", redactURL(err))
	}

	tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   r.chainID,
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: feeCap(head.BaseFee, tip),
		Gas:       gas * 12 / 10, // node estimate + 20%
		To:        &r.token,
		Data:      data,
	}), types.LatestSignerForChainID(r.chainID), r.key)
	if err != nil {
		return common.Hash{}, 0, err
	}
	if err := r.c.SendTransaction(ctx, tx); err != nil {
		return common.Hash{}, 0, fmt.Errorf("sending transaction: %w", redactURL(err))
	}
	return tx.Hash(), nonce, nil
}

// feeCap leaves room for the base fee to double before the tx is priced out.
func feeCap(baseFee, tip *big.Int) *big.Int {
	if baseFee == nil {
		baseFee = new(big.Int)
	}
	return new(big.Int).Add(new(big.Int).Mul(baseFee, big.NewInt(2)), tip)
}
