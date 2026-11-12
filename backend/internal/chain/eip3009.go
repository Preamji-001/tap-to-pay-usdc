package chain

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
)

var transferWithAuthorizationTypeHash = crypto.Keccak256Hash([]byte(
	"TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)",
))

// TransferAuthorization is the EIP-3009 message a payer signs to let anyone
// move Value of their tokens to To, once, inside the validity window.
type TransferAuthorization struct {
	From, To                common.Address
	Value                   *big.Int
	ValidAfter, ValidBefore *big.Int // unix seconds
	Nonce                   [32]byte
}

// Digest is the EIP-712 hash the payer signs:
// keccak256(0x1901 ‖ domainSeparator ‖ structHash).
func (a TransferAuthorization) Digest(domainSeparator common.Hash) common.Hash {
	structHash := crypto.Keccak256(
		transferWithAuthorizationTypeHash.Bytes(),
		common.LeftPadBytes(a.From.Bytes(), 32),
		common.LeftPadBytes(a.To.Bytes(), 32),
		math.U256Bytes(new(big.Int).Set(a.Value)),
		math.U256Bytes(new(big.Int).Set(a.ValidAfter)),
		math.U256Bytes(new(big.Int).Set(a.ValidBefore)),
		a.Nonce[:],
	)
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domainSeparator.Bytes(), structHash)
}

var (
	ErrSignatureLength = errors.New("signature must be 65 bytes")
	ErrSignatureV      = errors.New("signature v must be 27 or 28")
	ErrSignatureHighS  = errors.New("signature s must be in the lower half of the curve order")
)

var secp256k1HalfN = new(big.Int).Rsh(crypto.S256().Params().N, 1)

// RecoverSigner returns the address that produced sig (r ‖ s ‖ v) over
// digest. Only canonical signatures are accepted: v in {27, 28} and low s,
// the same rules the USDC contract applies.
func RecoverSigner(digest common.Hash, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, ErrSignatureLength
	}
	v := sig[64]
	if v != 27 && v != 28 {
		return common.Address{}, ErrSignatureV
	}
	if new(big.Int).SetBytes(sig[32:64]).Cmp(secp256k1HalfN) > 0 {
		return common.Address{}, ErrSignatureHighS
	}

	raw := make([]byte, 65)
	copy(raw, sig)
	raw[64] = v - 27
	pub, err := crypto.SigToPub(digest.Bytes(), raw)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}
