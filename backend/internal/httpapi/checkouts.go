package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
	"github.com/Preamji-001/crypto-pos-base/internal/money"
)

const maxBodyBytes = 4 << 10

// The only USDC fields we show; decimals checked on-chain.
const (
	tokenSymbol   = "USDC"
	tokenDecimals = 6
)

type createRequest struct {
	Amount            string `json:"amount"`
	MerchantReference string `json:"merchant_reference"`
}

type tokenJSON struct {
	Symbol   string `json:"symbol"`
	Address  string `json:"address"`
	Decimals int    `json:"decimals"`
}

type checkoutJSON struct {
	ID              string    `json:"id"`
	CheckoutURL     string    `json:"checkout_url"`
	Status          string    `json:"status"`
	Amount          string    `json:"amount"`
	AmountBaseUnits string    `json:"amount_base_units"`
	Token           tokenJSON `json:"token"`
	Merchant        string    `json:"merchant"`
	// ChainID (and Token) is the chain TypedData is for: the bound chain, the
	// one asked for with ?chain=, or the primary network.
	ChainID int64 `json:"chain_id"`
	// ChainBound is true once an authorization has fixed the chain. Until
	// then the customer may pay on any enabled chain.
	ChainBound        bool    `json:"chain_bound"`
	MerchantReference string  `json:"merchant_reference"`
	CreatedAt         string  `json:"created_at"`
	ExpiresAt         string  `json:"expires_at"`
	TxHash            *string `json:"tx_hash"`
	// TypedData is what the customer signs with eth_signTypedData_v4, after
	// adding message.from. Rebuilt from the stored checkout on every read.
	TypedData typedDataJSON `json:"typed_data"`
}

type typedDataJSON struct {
	Domain      domainJSON              `json:"domain"`
	Types       map[string][]typedField `json:"types"`
	PrimaryType string                  `json:"primaryType"`
	Message     map[string]string       `json:"message"`
}

type domainJSON struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           int64  `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

type typedField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var transferWithAuthorizationFields = []typedField{
	{"from", "address"}, {"to", "address"}, {"value", "uint256"},
	{"validAfter", "uint256"}, {"validBefore", "uint256"}, {"nonce", "bytes32"},
}

// typedData leaves out message.from: the payer is unknown until they sign.
// uint256 values are decimal strings so JavaScript never rounds them.
// The domain is the one for chain n.
func typedData(c *checkout.Checkout, n chain.Network) typedDataJSON {
	d := n.TokenParams()
	a := c.Authorization(common.Address{})
	return typedDataJSON{
		Domain:      domainJSON{Name: d.Name, Version: d.Version, ChainID: d.ChainID, VerifyingContract: d.Address.Hex()},
		Types:       map[string][]typedField{"TransferWithAuthorization": transferWithAuthorizationFields},
		PrimaryType: "TransferWithAuthorization",
		Message: map[string]string{
			"to":          a.To.Hex(),
			"value":       a.Value.String(),
			"validAfter":  a.ValidAfter.String(),
			"validBefore": a.ValidBefore.String(),
			"nonce":       hexutil.Encode(a.Nonce[:]),
		},
	}
}

// toJSON renders c with the typed data for chainID (0: the default).
func (s *server) toJSON(c *checkout.Checkout, chainID int64) (checkoutJSON, error) {
	n, err := s.svc.NetworkFor(c, chainID)
	if err != nil {
		return checkoutJSON{}, err
	}
	out := checkoutJSON{
		ID:                c.ID,
		CheckoutURL:       s.svc.CheckoutURL(c.ID),
		Status:            string(c.Status),
		Amount:            money.FormatBaseUnits(c.AmountBaseUnits, tokenDecimals),
		AmountBaseUnits:   c.AmountBaseUnits.String(),
		Token:             tokenJSON{Symbol: tokenSymbol, Address: n.USDC.Hex(), Decimals: tokenDecimals},
		Merchant:          c.Merchant.Hex(),
		ChainID:           n.ChainID,
		ChainBound:        c.Bound(),
		MerchantReference: c.MerchantReference,
		CreatedAt:         c.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:         c.ExpiresAt.UTC().Format(time.RFC3339),
		TypedData:         typedData(c, n),
	}
	if c.TxHash != nil {
		h := c.TxHash.Hex()
		out.TxHash = &h
	}
	return out, nil
}

// writeCheckout writes c, with the typed data for chainID, as status. A chain
// the client asked for that isn't enabled is a 400 and one the checkout isn't
// bound to a 409. A bound chain that is no longer enabled is a 500: a server
// configuration problem.
func (s *server) writeCheckout(w http.ResponseWriter, r *http.Request, status int, c *checkout.Checkout, chainID int64) {
	out, err := s.toJSON(c, chainID)
	switch {
	case errors.Is(err, checkout.ErrUnknownChain):
		writeError(w, http.StatusBadRequest, "unknown_chain", "that chain is not accepted by this server")
	case errors.Is(err, checkout.ErrChainMismatch):
		writeError(w, http.StatusConflict, "chain_mismatch", "this checkout is already being paid on another chain")
	case err != nil:
		writeInternal(w, r, err)
	default:
		writeJSON(w, status, out)
	}
}

// parseChainID reads an optional positive chain ID; "" means 0 (the default).
func parseChainID(v string) (int64, bool) {
	if v == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(v, 10, 64)
	return id, err == nil && id > 0
}

func (s *server) createCheckout(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	// Unknown fields (merchant, token, chain_id, ...) are an error, not ignored:
	// those values come only from server config.
	if err := decodeStrict(w, r, &req); errors.Is(err, errTrailingJSON) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be a single JSON object")
		return
	} else if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be JSON with only amount and merchant_reference")
		return
	}

	c, err := s.svc.Create(r.Context(), checkout.CreateRequest{
		Amount:            req.Amount,
		MerchantReference: req.MerchantReference,
	})
	switch {
	case errors.Is(err, checkout.ErrInvalidAmount):
		writeError(w, http.StatusBadRequest, "invalid_amount", "amount must be a positive decimal string with at most 6 decimal places, like \"12.50\"")
	case errors.Is(err, checkout.ErrAmountTooLarge):
		writeError(w, http.StatusBadRequest, "amount_too_large", err.Error())
	case errors.Is(err, checkout.ErrInvalidReference):
		writeError(w, http.StatusBadRequest, "invalid_reference", err.Error())
	case errors.Is(err, checkout.ErrChainUnavailable):
		slog.Warn("create: chain unavailable", "err", err)
		writeError(w, http.StatusServiceUnavailable, "chain_unavailable", "could not reach the chain; try again")
	case err != nil:
		writeInternal(w, r, err)
	default:
		w.Header().Set("Location", "/api/checkouts/"+c.ID)
		s.writeCheckout(w, r, http.StatusCreated, c, 0)
	}
}

// getCheckout returns the checkout, with typed data for ?chain=<id> when
// given: the page asks for the chain the customer is about to sign on.
func (s *server) getCheckout(w http.ResponseWriter, r *http.Request) {
	chainID, ok := parseChainID(r.URL.Query().Get("chain"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "chain must be a positive decimal chain ID")
		return
	}
	c, err := s.svc.Get(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, checkout.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "checkout not found")
	case err != nil:
		writeInternal(w, r, err)
	default:
		s.writeCheckout(w, r, http.StatusOK, c, chainID)
	}
}

type authorizationRequest struct {
	Payer     string `json:"payer"`
	Signature string `json:"signature"`
	// ChainID is the chain the signature is for. Omitted (0) means the
	// default: the bound chain, or the primary network.
	ChainID int64 `json:"chain_id"`
}

func (s *server) authorizeCheckout(w http.ResponseWriter, r *http.Request) {
	var req authorizationRequest
	// Amount, recipient, nonce and validity are rebuilt from the database;
	// a body that also sends them is rejected, never silently ignored.
	if err := decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be a single JSON object with only payer, signature and chain_id")
		return
	}
	if req.ChainID < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "chain_id must be a positive chain ID")
		return
	}
	if !strings.HasPrefix(req.Payer, "0x") || !common.IsHexAddress(req.Payer) {
		writeError(w, http.StatusBadRequest, "invalid_request", "payer must be a 0x-prefixed 20-byte hex address")
		return
	}
	sig, err := hexutil.Decode(req.Signature)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "signature must be 0x-prefixed hex")
		return
	}

	c, err := s.svc.Authorize(r.Context(), r.PathValue("id"), req.ChainID, common.HexToAddress(req.Payer), sig)
	var revert *chain.RevertError
	switch {
	case errors.Is(err, checkout.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "checkout not found")
	case errors.Is(err, checkout.ErrInvalidSignature):
		writeError(w, http.StatusBadRequest, "invalid_signature", err.Error())
	case errors.Is(err, checkout.ErrNotPending):
		writeError(w, http.StatusConflict, "not_pending", err.Error())
	case errors.Is(err, checkout.ErrExpired):
		writeError(w, http.StatusConflict, "expired", err.Error())
	case errors.Is(err, checkout.ErrUnknownChain):
		writeError(w, http.StatusBadRequest, "unknown_chain", "that chain is not accepted by this server")
	case errors.Is(err, checkout.ErrChainMismatch):
		writeError(w, http.StatusConflict, "chain_mismatch", "this checkout is already being paid on another chain")
	case errors.Is(err, checkout.ErrUnsupportedChain):
		slog.Warn("authorization for a chain that is not enabled", "id", r.PathValue("id"), "err", err)
		writeError(w, http.StatusConflict, "unsupported_chain", "this checkout's chain is not accepted by this server")
	case errors.Is(err, checkout.ErrInsufficientBalance):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_balance", err.Error())
	case errors.Is(err, checkout.ErrSimulationFailed) && errors.As(err, &revert):
		writeError(w, http.StatusUnprocessableEntity, "simulation_failed", revert.Reason)
	case errors.Is(err, checkout.ErrChainUnavailable):
		slog.Warn("authorization pre-flight failed", "id", r.PathValue("id"), "err", err)
		writeError(w, http.StatusServiceUnavailable, "chain_unavailable", "could not reach the chain; try again")
	case err != nil:
		writeInternal(w, r, err)
	default:
		s.writeCheckout(w, r, http.StatusAccepted, c, 0)
	}
}

type routesRequest struct {
	Payer string `json:"payer"`
	// PreferredChainID is the customer's choice, or the chain their wallet
	// is on. Optional; a chain this server doesn't enable is ignored.
	PreferredChainID int64 `json:"preferred_chain_id"`
}

type routeJSON struct {
	ChainID int64  `json:"chain_id"`
	Network string `json:"network"`
	// Balance is the payer's USDC on this chain, or null when the chain
	// didn't answer (unknown, not zero).
	Balance    *string `json:"balance"`
	Sufficient bool    `json:"sufficient"`
}

type routesJSON struct {
	Amount string      `json:"amount"`
	Routes []routeJSON `json:"routes"`
	// RecommendedChainID is the chain to pay on, or null when no chain is
	// known to hold enough.
	RecommendedChainID *int64 `json:"recommended_chain_id"`
}

// checkoutRoutes tells the page where the payer can pay: their USDC on each
// enabled chain and the recommended chain. It binds nothing.
func (s *server) checkoutRoutes(w http.ResponseWriter, r *http.Request) {
	var req routesRequest
	if err := decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be a single JSON object with only payer and preferred_chain_id")
		return
	}
	if !strings.HasPrefix(req.Payer, "0x") || !common.IsHexAddress(req.Payer) {
		writeError(w, http.StatusBadRequest, "invalid_request", "payer must be a 0x-prefixed 20-byte hex address")
		return
	}
	routes, err := s.svc.Routes(r.Context(), r.PathValue("id"), common.HexToAddress(req.Payer), req.PreferredChainID)
	switch {
	case errors.Is(err, checkout.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "checkout not found")
		return
	case errors.Is(err, checkout.ErrNotPending):
		writeError(w, http.StatusConflict, "not_pending", err.Error())
		return
	case errors.Is(err, checkout.ErrExpired):
		writeError(w, http.StatusConflict, "expired", err.Error())
		return
	case err != nil:
		writeInternal(w, r, err)
		return
	}
	out := routesJSON{Amount: money.FormatBaseUnits(routes.Amount, tokenDecimals), Routes: []routeJSON{}}
	for _, rt := range routes.Routes {
		j := routeJSON{ChainID: rt.Network.ChainID, Network: rt.Network.Name, Sufficient: rt.Sufficient}
		if rt.Balance != nil {
			b := money.FormatBaseUnits(rt.Balance, tokenDecimals)
			j.Balance = &b
		}
		out.Routes = append(out.Routes, j)
	}
	if routes.Recommended != 0 {
		out.RecommendedChainID = &routes.Recommended
	}
	writeJSON(w, http.StatusOK, out)
}

var errTrailingJSON = errors.New("trailing data after JSON object")

// decodeStrict decodes exactly one JSON object with no unknown fields.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errTrailingJSON
	}
	return nil
}
