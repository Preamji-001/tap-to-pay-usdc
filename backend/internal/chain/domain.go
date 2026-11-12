// Package chain holds the on-chain pieces: EIP-712 hashing and the checks
// that the configured token is the one we expect.
package chain

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
)

var eip712DomainTypeHash = crypto.Keccak256Hash(
	[]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
)

// DomainSeparator computes the EIP-712 domain separator:
// keccak256(abi.encode(typeHash, keccak256(name), keccak256(version), chainId, verifyingContract)).
// Every typed-data signature for the token is bound to this value.
func DomainSeparator(name, version string, chainID int64, verifyingContract common.Address) common.Hash {
	return crypto.Keccak256Hash(
		eip712DomainTypeHash.Bytes(),
		crypto.Keccak256([]byte(name)),
		crypto.Keccak256([]byte(version)),
		math.U256Bytes(big.NewInt(chainID)),
		common.LeftPadBytes(verifyingContract.Bytes(), 32),
	)
}
