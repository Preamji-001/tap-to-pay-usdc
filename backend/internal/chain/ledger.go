package chain

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	authorizationUsedTopic = crypto.Keccak256Hash([]byte("AuthorizationUsed(address,bytes32)"))
	transferTopic          = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
)

// logChunk keeps each eth_getLogs range small enough for public RPC providers.
const logChunk = 500

// AuthorizationLog is where an EIP-3009 authorization was used on chain.
type AuthorizationLog struct {
	Payer  common.Address
	TxHash common.Hash
	Block  uint64
}

// FindAuthorizationUsed searches the token's AuthorizationUsed logs for nonce
// (and payer, when known) from fromBlock to the head. It returns nil when
// there is none yet.
func (u *USDC) FindAuthorizationUsed(ctx context.Context, payer *common.Address, nonce [32]byte, fromBlock uint64) (*AuthorizationLog, error) {
	head, err := u.BlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	topics := [][]common.Hash{{authorizationUsedTopic}, nil, {common.Hash(nonce)}}
	if payer != nil {
		topics[1] = []common.Hash{common.BytesToHash(payer.Bytes())}
	}
	for from := fromBlock; from <= head; from += logChunk {
		to := min(from+logChunk-1, head)
		logs, err := u.r.FilterLogs(ctx, ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(from),
			ToBlock:   new(big.Int).SetUint64(to),
			Addresses: []common.Address{u.token},
			Topics:    topics,
		})
		if err != nil {
			return nil, fmt.Errorf("searching AuthorizationUsed logs: %w", redactURL(err))
		}
		for _, l := range logs {
			if !l.Removed && len(l.Topics) == 3 {
				return &AuthorizationLog{Payer: common.BytesToAddress(l.Topics[1].Bytes()), TxHash: l.TxHash, Block: l.BlockNumber}, nil
			}
		}
	}
	return nil, nil
}

// Receipt returns the transaction's receipt, or nil while it isn't mined.
func (u *USDC) Receipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	r, err := u.r.TransactionReceipt(ctx, hash)
	if errors.Is(err, ethereum.NotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading receipt: %w", redactURL(err))
	}
	return r, nil
}

// TransactionTo reports whether the node knows the transaction, and its
// destination.
func (u *USDC) TransactionTo(ctx context.Context, hash common.Hash) (*common.Address, bool, error) {
	tx, _, err := u.r.TransactionByHash(ctx, hash)
	if errors.Is(err, ethereum.NotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading transaction: %w", redactURL(err))
	}
	return tx.To(), true, nil
}

func (u *USDC) BlockNumber(ctx context.Context) (uint64, error) {
	n, err := u.r.BlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading block number: %w", redactURL(err))
	}
	return n, nil
}

// ExpectedTransfer is what a confirmed checkout must have moved on chain.
type ExpectedTransfer struct {
	Token, Payer, Merchant common.Address
	Amount                 *big.Int
	Nonce                  [32]byte
}

// ErrVerification means a mined transaction doesn't prove the payment.
var ErrVerification = errors.New("payment not verified")

// VerifyTransfer confirms a receipt proves the payment: it succeeded, and the
// token itself emitted both AuthorizationUsed(payer, nonce) and
// Transfer(payer, merchant, amount). Logs from any other address are ignored,
// so a contract can't fake them.
func VerifyTransfer(r *types.Receipt, want ExpectedTransfer) error {
	if r.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("%w: transaction reverted", ErrVerification)
	}
	var authorized, transferred bool
	for _, l := range r.Logs {
		if l.Address != want.Token || len(l.Topics) == 0 {
			continue
		}
		switch {
		case l.Topics[0] == authorizationUsedTopic && len(l.Topics) == 3:
			authorized = authorized ||
				(common.BytesToAddress(l.Topics[1].Bytes()) == want.Payer && l.Topics[2] == common.Hash(want.Nonce))
		case l.Topics[0] == transferTopic && len(l.Topics) == 3 && len(l.Data) == 32:
			transferred = transferred ||
				(common.BytesToAddress(l.Topics[1].Bytes()) == want.Payer &&
					common.BytesToAddress(l.Topics[2].Bytes()) == want.Merchant &&
					new(big.Int).SetBytes(l.Data).Cmp(want.Amount) == 0)
		}
	}
	switch {
	case !authorized:
		return fmt.Errorf("%w: no AuthorizationUsed(payer, nonce) log from the token", ErrVerification)
	case !transferred:
		return fmt.Errorf("%w: no Transfer(payer, merchant, amount) log from the token", ErrVerification)
	}
	return nil
}
