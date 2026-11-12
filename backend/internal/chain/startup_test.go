package chain

import (
	"context"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

type fakeReader struct {
	chainID    *big.Int
	chainIDErr error
	code       []byte
	domain     []byte
}

func (f fakeReader) ChainID(context.Context) (*big.Int, error) { return f.chainID, f.chainIDErr }

func (f fakeReader) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return f.code, nil
}

func (f fakeReader) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if common.Bytes2Hex(msg.Data) != "3644e515" {
		return nil, errors.New("unexpected call data " + common.Bytes2Hex(msg.Data))
	}
	return f.domain, nil
}

func goodReader() fakeReader {
	return fakeReader{
		chainID: big.NewInt(11155111),
		code:    []byte{0x60, 0x80},
		domain:  sepoliaUSDCDomain.Bytes(),
	}
}

var sepoliaParams = TokenParams{ChainID: 11155111, Address: sepoliaUSDC, Name: "USDC", Version: "2"}

func TestCheckToken(t *testing.T) {
	rpcDown := errors.New("rpc down")
	tests := []struct {
		name    string
		mutate  func(*fakeReader)
		wantErr string
		wantIs  error
	}{
		{name: "all good", mutate: func(*fakeReader) {}},
		{name: "wrong chain", mutate: func(f *fakeReader) { f.chainID = big.NewInt(1) }, wantErr: "chain id"},
		{name: "no code", mutate: func(f *fakeReader) { f.code = nil }, wantErr: "no code"},
		{name: "wrong domain", mutate: func(f *fakeReader) { f.domain = make([]byte, 32) }, wantErr: "domain separator"},
		{name: "rpc error", mutate: func(f *fakeReader) { f.chainIDErr = rpcDown }, wantIs: rpcDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := goodReader()
			tt.mutate(&r)
			err := CheckToken(context.Background(), r, sepoliaParams)
			switch {
			case tt.wantErr == "" && tt.wantIs == nil:
				if err != nil {
					t.Fatalf("CheckToken: %v", err)
				}
			case tt.wantIs != nil:
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("CheckToken = %v, want wrapped %v", err, tt.wantIs)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("CheckToken = %v, want error containing %q", err, tt.wantErr)
				}
			}
		})
	}
}

func TestCheckTokenRedactsRPCURL(t *testing.T) {
	r := goodReader()
	r.chainIDErr = &url.Error{Op: "Post", URL: "https://sepolia.example/v3/SECRETKEY123", Err: errors.New("dial tcp: connection refused")}
	err := CheckToken(context.Background(), r, sepoliaParams)
	if err == nil {
		t.Fatal("CheckToken succeeded with a failing RPC")
	}
	if strings.Contains(err.Error(), "SECRETKEY123") || strings.Contains(err.Error(), "sepolia.example") {
		t.Errorf("error leaks the RPC URL: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error lost the cause: %v", err)
	}
}
