package chain

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
)

// The slice of the USDC (FiatTokenV2) ABI the pre-flight needs.
var usdcABI = mustParseABI(`[
	{"type":"function","name":"authorizationState","stateMutability":"view",
	 "inputs":[{"name":"authorizer","type":"address"},{"name":"nonce","type":"bytes32"}],
	 "outputs":[{"name":"","type":"bool"}]},
	{"type":"function","name":"balanceOf","stateMutability":"view",
	 "inputs":[{"name":"account","type":"address"}],
	 "outputs":[{"name":"","type":"uint256"}]},
	{"type":"function","name":"transferWithAuthorization","stateMutability":"nonpayable",
	 "inputs":[{"name":"from","type":"address"},{"name":"to","type":"address"},{"name":"value","type":"uint256"},
	           {"name":"validAfter","type":"uint256"},{"name":"validBefore","type":"uint256"},{"name":"nonce","type":"bytes32"},
	           {"name":"v","type":"uint8"},{"name":"r","type":"bytes32"},{"name":"s","type":"bytes32"}],
	 "outputs":[]}
]`)

func mustParseABI(s string) abi.ABI {
	a, err := abi.JSON(strings.NewReader(s))
	if err != nil {
		panic(err)
	}
	return a
}

// RevertError means the contract call ran and reverted: the request is
// wrong, not the network.
type RevertError struct{ Reason string }

func (e *RevertError) Error() string { return "execution reverted: " + e.Reason }

// USDC reads the token contract and its logs. It never sends anything.
type USDC struct {
	r     Client
	token common.Address
}

func NewUSDC(c Client, token common.Address) *USDC { return &USDC{r: c, token: token} }

// AuthorizationUsed reports whether payer's authorization with nonce has
// already been used or cancelled on chain.
func (u *USDC) AuthorizationUsed(ctx context.Context, payer common.Address, nonce [32]byte) (bool, error) {
	var used bool
	err := u.call(ctx, &used, "authorizationState", payer, nonce)
	return used, err
}

func (u *USDC) BalanceOf(ctx context.Context, account common.Address) (*big.Int, error) {
	var bal *big.Int
	err := u.call(ctx, &bal, "balanceOf", account)
	return bal, err
}

// SimulateTransfer runs transferWithAuthorization as an eth_call, so nothing
// is broadcast. A revert comes back as *RevertError.
func (u *USDC) SimulateTransfer(ctx context.Context, a TransferAuthorization, sig []byte) error {
	data, err := packTransfer(a, sig)
	if err != nil {
		return err
	}
	_, err = u.r.CallContract(ctx, ethereum.CallMsg{To: &u.token, Data: data}, nil)
	return classifyCallError(err)
}

// packTransfer is the calldata of transferWithAuthorization, with v, r and s
// split from the 65-byte signature.
func packTransfer(a TransferAuthorization, sig []byte) ([]byte, error) {
	if len(sig) != 65 {
		return nil, ErrSignatureLength
	}
	var r, s [32]byte
	copy(r[:], sig[:32])
	copy(s[:], sig[32:64])
	return usdcABI.Pack("transferWithAuthorization",
		a.From, a.To, a.Value, a.ValidAfter, a.ValidBefore, a.Nonce, sig[64], r, s)
}

func (u *USDC) call(ctx context.Context, out any, method string, args ...any) error {
	data, err := usdcABI.Pack(method, args...)
	if err != nil {
		return err
	}
	res, err := u.r.CallContract(ctx, ethereum.CallMsg{To: &u.token, Data: data}, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", method, classifyCallError(err))
	}
	if err := usdcABI.UnpackIntoInterface(out, method, res); err != nil {
		return fmt.Errorf("%s: decoding result: %w", method, err)
	}
	return nil
}

// classifyCallError turns a node's "execution reverted" into *RevertError
// and redacts the RPC URL from anything else.
func classifyCallError(err error) error {
	if err == nil {
		return nil
	}
	var de rpc.DataError
	if errors.As(err, &de) {
		if s, ok := de.ErrorData().(string); ok {
			if data, decErr := hexutil.Decode(s); decErr == nil {
				if reason, unpackErr := abi.UnpackRevert(data); unpackErr == nil {
					return &RevertError{Reason: reason}
				}
			}
		}
	}
	if msg := err.Error(); strings.HasPrefix(msg, "execution reverted") {
		return &RevertError{Reason: strings.TrimPrefix(strings.TrimPrefix(msg, "execution reverted"), ": ")}
	}
	return redactURL(err)
}
