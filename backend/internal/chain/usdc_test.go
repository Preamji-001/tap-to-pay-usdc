package chain

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// callReader is a Reader whose CallContract records the call and returns
// canned output.
type callReader struct {
	Client // unimplemented methods panic
	got    ethereum.CallMsg
	out    []byte
	err    error
}

func (r *callReader) ChainID(context.Context) (*big.Int, error) { return big.NewInt(11155111), nil }
func (r *callReader) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return []byte{1}, nil
}
func (r *callReader) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	r.got = msg
	return r.out, r.err
}

// revertErr looks like what ethclient returns for a reverted eth_call.
type revertErr struct{ msg, data string }

func (e revertErr) Error() string          { return e.msg }
func (e revertErr) ErrorCode() int         { return 3 }
func (e revertErr) ErrorData() interface{} { return e.data }

func selector(sig string) []byte { return crypto.Keccak256([]byte(sig))[:4] }

func word(b []byte) []byte { return common.LeftPadBytes(b, 32) }

func TestAuthorizationUsed_EncodesAndDecodes(t *testing.T) {
	payer := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	var nonce [32]byte
	nonce[0] = 0x42
	for _, want := range []bool{true, false} {
		out := word(nil)
		if want {
			out = word([]byte{1})
		}
		r := &callReader{out: out}
		got, err := NewUSDC(r, sepoliaUSDC).AuthorizationUsed(context.Background(), payer, nonce)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("AuthorizationUsed = %v, want %v", got, want)
		}
		wantData := append(selector("authorizationState(address,bytes32)"), append(word(payer.Bytes()), nonce[:]...)...)
		if *r.got.To != sepoliaUSDC || !bytes.Equal(r.got.Data, wantData) {
			t.Errorf("call = to %s data %x", r.got.To, r.got.Data)
		}
	}
}

func TestBalanceOf_EncodesAndDecodes(t *testing.T) {
	acct := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	r := &callReader{out: word(big.NewInt(12_500_000).Bytes())}
	got, err := NewUSDC(r, sepoliaUSDC).BalanceOf(context.Background(), acct)
	if err != nil {
		t.Fatal(err)
	}
	if got.Int64() != 12_500_000 {
		t.Errorf("BalanceOf = %s", got)
	}
	if want := append(selector("balanceOf(address)"), word(acct.Bytes())...); !bytes.Equal(r.got.Data, want) {
		t.Errorf("data = %x", r.got.Data)
	}
}

func TestSimulateTransfer_EncodesAllNineArgs(t *testing.T) {
	a := testAuthorization(common.HexToAddress("0x00000000000000000000000000000000000000aa"))
	sig := make([]byte, 65)
	for i := range 64 {
		sig[i] = byte(i + 1)
	}
	sig[64] = 28
	r := &callReader{}
	if err := NewUSDC(r, sepoliaUSDC).SimulateTransfer(context.Background(), a, sig); err != nil {
		t.Fatal(err)
	}

	want := selector("transferWithAuthorization(address,address,uint256,uint256,uint256,bytes32,uint8,bytes32,bytes32)")
	for _, w := range [][]byte{
		word(a.From.Bytes()), word(a.To.Bytes()), word(a.Value.Bytes()),
		word(a.ValidAfter.Bytes()), word(a.ValidBefore.Bytes()), a.Nonce[:],
		word([]byte{28}), sig[:32], sig[32:64],
	} {
		want = append(want, w...)
	}
	if !bytes.Equal(r.got.Data, want) {
		t.Fatalf("data =\n%x\nwant\n%x", r.got.Data, want)
	}
}

func TestSimulateTransfer_RevertIsRevertError(t *testing.T) {
	a := testAuthorization(common.Address{})
	// Error(string) revert data for "FiatTokenV2: invalid signature".
	reason := "FiatTokenV2: invalid signature"
	data := append(selector("Error(string)"), word([]byte{0x20})...)
	data = append(data, word(big.NewInt(int64(len(reason))).Bytes())...)
	data = append(data, common.RightPadBytes([]byte(reason), 32)...)

	for name, err := range map[string]error{
		"with revert data": revertErr{msg: "execution reverted: " + reason, data: hexutil.Encode(data)},
		"message only":     revertErr{msg: "execution reverted: " + reason},
	} {
		r := &callReader{err: err}
		got := NewUSDC(r, sepoliaUSDC).SimulateTransfer(context.Background(), a, make([]byte, 65))
		var re *RevertError
		if !errors.As(got, &re) || re.Reason != reason {
			t.Errorf("%s: err = %#v, want RevertError %q", name, got, reason)
		}
	}
}

func TestSimulateTransfer_RPCErrorIsNotRevert(t *testing.T) {
	secret := "https://sepolia.example/v3/SECRETKEY"
	r := &callReader{err: &url.Error{Op: "Post", URL: secret, Err: errors.New("connection refused")}}
	err := NewUSDC(r, sepoliaUSDC).SimulateTransfer(context.Background(), testAuthorization(common.Address{}), make([]byte, 65))
	var re *RevertError
	if err == nil || errors.As(err, &re) {
		t.Fatalf("err = %v, want a plain error", err)
	}
	if strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("error leaks the RPC URL: %v", err)
	}
}
