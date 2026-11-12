package checkout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/Preamji-001/crypto-pos-base/internal/chain"
)

const (
	reconcileInterval = 3 * time.Second
	// sendMargin: don't send an authorization this close to its validBefore;
	// it could be mined after it has expired.
	sendMargin = 30 * time.Second
	// dropAfter: a sent tx the node no longer knows about after this long is
	// treated as dropped and resent at the same account nonce.
	dropAfter = 10 * time.Minute
	// unknownBlockLookback bounds the log search for rows created before
	// created_block was recorded.
	unknownBlockLookback = 10_000
)

// Sender broadcasts an authorization from the relayer account.
// *chain.Relayer satisfies it.
type Sender interface {
	Send(ctx context.Context, a chain.TransferAuthorization, sig []byte, reuseNonce *uint64) (common.Hash, uint64, error)
}

// Lane is one chain the reconciler settles on: the network's facts (its USDC
// address and confirmation depth), the reads for that chain, and the relayer
// that sends there. Every lane has its own relayer, so each chain keeps its
// own account nonce and send lock.
type Lane struct {
	Network chain.Network
	Chain   Chain
	Sender  Sender
}

// Reconciler moves every non-terminal checkout towards confirmed, failed or
// expired: it sends accepted authorizations, verifies receipts and logs, and
// recovers from crashes, dropped transactions and payments made by others.
// Each checkout is handled only by the lane for its own chain. It is the only
// writer after a checkout leaves pending, and it runs alone.
type Reconciler struct {
	repo  Repository
	lanes map[int64]*laneReconciler
	order []int64 // lane chain IDs, in the order given
	now   func() time.Time
	nudge chan struct{}
}

// laneReconciler reconciles the checkouts of one chain.
type laneReconciler struct {
	repo          Repository
	network       chain.Network
	chain         Chain
	sender        Sender
	token         common.Address
	now           func() time.Time
	confirmations uint64
}

// NewReconciler panics if two lanes share a chain ID: that is a wiring bug,
// and one of them would silently never run.
func NewReconciler(repo Repository, lanes []Lane, now func() time.Time) *Reconciler {
	r := &Reconciler{repo: repo, lanes: make(map[int64]*laneReconciler, len(lanes)), now: now, nudge: make(chan struct{}, 1)}
	for _, l := range lanes {
		if _, dup := r.lanes[l.Network.ChainID]; dup {
			panic(fmt.Sprintf("reconciler: two lanes for chain %d", l.Network.ChainID))
		}
		r.order = append(r.order, l.Network.ChainID)
		r.lanes[l.Network.ChainID] = &laneReconciler{
			repo: repo, network: l.Network, chain: l.Chain, sender: l.Sender, token: l.Network.USDC,
			now: now, confirmations: max(l.Network.Confirmations, 1),
		}
	}
	return r
}

// Submit implements Relayer: it wakes the loop so a just-accepted checkout is
// sent now instead of on the next tick. It never blocks the HTTP request.
func (r *Reconciler) Submit(context.Context, *Checkout) {
	select {
	case r.nudge <- struct{}{}:
	default:
	}
}

// Run reconciles every few seconds, and on every nudge, until ctx ends.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		r.Step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.nudge:
		}
	}
}

// Step makes one pass over the active checkouts. Checkouts are grouped by
// chain and every chain is worked in parallel, so a slow or failing RPC on
// one chain never delays another. An error on one checkout is logged and
// retried next pass; it never fails the checkout by itself.
func (r *Reconciler) Step(ctx context.Context) {
	active, err := r.repo.ListActive(ctx)
	if err != nil {
		slog.Error("reconciler: listing checkouts", "err", err)
		return
	}
	byChain := make(map[int64][]*Checkout)
	for _, c := range active {
		byChain[c.ChainID] = append(byChain[c.ChainID], c)
	}
	var wg sync.WaitGroup
	if unbound := byChain[0]; len(unbound) > 0 {
		delete(byChain, 0)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, c := range unbound {
				if err := r.reconcileUnbound(ctx, c); err != nil {
					slog.Warn("reconciler: will retry", "id", c.ID, "status", c.Status, "err", err)
				}
			}
		}()
	}
	for chainID, checkouts := range byChain {
		lane, ok := r.lanes[chainID]
		if !ok {
			// Its chain was disabled while it was active. Leave it untouched:
			// re-enabling the chain resumes it where it stopped.
			for _, c := range checkouts {
				slog.Warn("reconciler: no lane for the checkout's chain; skipping", "id", c.ID, "chain_id", chainID, "status", c.Status)
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, c := range checkouts {
				if err := lane.reconcile(ctx, c); err != nil {
					slog.Warn("reconciler: will retry", "id", c.ID, "network", lane.network.Key, "status", c.Status, "err", err)
				}
			}
		}()
	}
	wg.Wait()
}

// reconcileUnbound handles a checkout nobody has authorized with us, so its
// chain isn't known. Before it is allowed to expire, every chain is searched
// for its nonce: the customer may have signed for one chain and someone else
// sent that signature without telling us. A payment found and verified there
// binds the checkout to that chain and confirms it. If any chain can't be
// searched, the checkout stays pending and is retried: a payment can't be
// ruled out yet.
func (r *Reconciler) reconcileUnbound(ctx context.Context, c *Checkout) error {
	if c.Status != StatusPending {
		// The store never allows this. Don't guess which chain it was on.
		return fmt.Errorf("%s checkout has no chain", c.Status)
	}
	if r.now().Before(c.ExpiresAt) {
		return nil
	}
	var errs []error
	for _, id := range r.order {
		lane := r.lanes[id]
		probe := *c
		probe.CreatedBlock = c.StartBlocks[id] // 0: unknown, fixed lookback
		found, err := lane.settleFromLog(ctx, &probe, nil)
		switch {
		case errors.Is(err, errUnverified):
			// A log with this nonce that doesn't prove our payment (another
			// payer can use the same nonce value). Keep looking elsewhere.
			slog.Warn("reconciler: unverified authorization log for an unbound checkout", "id", c.ID, "network", lane.network.Key)
		case found:
			return err
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", lane.network.Key, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	ok, err := r.repo.Transition(ctx, c.ID, c.Status, Update{Status: StatusExpired, At: r.now().UTC()})
	if err != nil {
		return fmt.Errorf("storing %s → expired: %w", c.Status, err)
	}
	if !ok {
		return fmt.Errorf("checkout left %s before expired could be stored", c.Status)
	}
	return nil
}

// errUnverified: an unbound checkout's nonce was used on a chain, but not
// by a transfer that pays this checkout.
var errUnverified = errors.New("authorization log does not verify")

func (r *laneReconciler) reconcile(ctx context.Context, c *Checkout) error {
	// A row whose token isn't this chain's USDC was never created by us for
	// this chain: don't send it or judge its logs here.
	if c.Token != r.token {
		return fmt.Errorf("token %s is not %s's USDC", c.Token.Hex(), r.network.Key)
	}
	// Accepted rows always carry these; a row without them can't be sent or
	// verified, and must not take the loop (or the server) down.
	switch {
	case c.Status != StatusPending && (c.Payer == nil || len(c.Signature) == 0):
		return r.fail(ctx, c, "stored checkout has no payer or signature")
	case c.Status == StatusSubmitted && c.TxHash == nil:
		return r.fail(ctx, c, "stored checkout has no transaction hash")
	}
	switch c.Status {
	case StatusPending:
		return r.reconcilePending(ctx, c)
	case StatusSubmitting:
		return r.reconcileSubmitting(ctx, c)
	case StatusSubmitted:
		return r.reconcileSubmitted(ctx, c)
	}
	return nil
}

// A pending checkout past its expiry may still have been paid by someone
// else holding the signature: look for its nonce on chain before expiring it.
func (r *laneReconciler) reconcilePending(ctx context.Context, c *Checkout) error {
	if r.now().Before(c.ExpiresAt) {
		return nil
	}
	found, err := r.settleFromLog(ctx, c, nil)
	if err != nil || found {
		return err
	}
	return r.transition(ctx, c, Update{Status: StatusExpired})
}

func (r *laneReconciler) reconcileSubmitting(ctx context.Context, c *Checkout) error {
	used, err := r.chain.AuthorizationUsed(ctx, *c.Payer, c.Nonce)
	if err != nil {
		return err
	}
	if used {
		// Sent before a crash, or by someone else: the log tells us which tx.
		_, err := r.settleFromLog(ctx, c, c.Payer)
		return err
	}
	if !r.now().Before(time.Unix(c.ValidBefore, 0).Add(-sendMargin)) {
		return r.fail(ctx, c, "authorization window passed before it was sent")
	}
	return r.send(ctx, c, nil)
}

func (r *laneReconciler) reconcileSubmitted(ctx context.Context, c *Checkout) error {
	receipt, err := r.chain.Receipt(ctx, *c.TxHash)
	if err != nil {
		return err
	}
	if receipt == nil {
		return r.checkDropped(ctx, c)
	}
	if deep, err := r.deepEnough(ctx, receipt); err != nil || !deep {
		return err
	}

	if receipt.Status != types.ReceiptStatusSuccessful {
		// Our tx reverted. If the authorization is used anyway, someone else
		// sent it first (a front-run): the customer still paid.
		used, err := r.chain.AuthorizationUsed(ctx, *c.Payer, c.Nonce)
		if err != nil {
			return err
		}
		if used {
			_, err := r.settleFromLog(ctx, c, c.Payer)
			return err
		}
		return r.fail(ctx, c, "transaction reverted")
	}

	to, _, err := r.chain.TransactionTo(ctx, *c.TxHash)
	if err != nil {
		return err
	}
	if to == nil || *to != r.token {
		return r.fail(ctx, c, "transaction was not sent to the token")
	}
	return r.settle(ctx, c, *c.Payer, *c.TxHash, receipt)
}

// checkDropped handles a sent tx with no receipt yet. A tx the node still
// knows is just waiting (fee bumping is out of scope). One the node has
// forgotten for dropAfter is resent at the same account nonce, unless the
// authorization was used meanwhile.
func (r *laneReconciler) checkDropped(ctx context.Context, c *Checkout) error {
	waited := r.now().Sub(c.UpdatedAt)
	_, known, err := r.chain.TransactionTo(ctx, *c.TxHash)
	if err != nil {
		return err
	}
	if known {
		if waited >= dropAfter {
			slog.Warn("reconciler: transaction stuck without a receipt; needs manual attention",
				"id", c.ID, "tx", c.TxHash.Hex(), "waited", waited.Round(time.Second))
		}
		return nil
	}
	if waited < dropAfter {
		return nil
	}
	used, err := r.chain.AuthorizationUsed(ctx, *c.Payer, c.Nonce)
	if err != nil {
		return err
	}
	if used {
		_, err := r.settleFromLog(ctx, c, c.Payer)
		return err
	}
	if !r.now().Before(time.Unix(c.ValidBefore, 0).Add(-sendMargin)) {
		return r.fail(ctx, c, "transaction dropped and the authorization window passed")
	}
	slog.Warn("reconciler: transaction dropped; resending at the same account nonce", "id", c.ID, "tx", c.TxHash.Hex())
	return r.send(ctx, c, c.RelayerNonce)
}

func (r *laneReconciler) send(ctx context.Context, c *Checkout, reuseNonce *uint64) error {
	hash, accountNonce, err := r.sender.Send(ctx, c.Authorization(*c.Payer), c.Signature, reuseNonce)
	var revert *chain.RevertError
	if errors.As(err, &revert) {
		// Used in the meantime (front-run): the next pass confirms it from the log.
		if used, usedErr := r.chain.AuthorizationUsed(ctx, *c.Payer, c.Nonce); usedErr != nil || used {
			return usedErr
		}
		return r.fail(ctx, c, "transfer would revert: "+revert.Reason)
	}
	if err != nil {
		return err
	}
	slog.Info("reconciler: sent", "id", c.ID, "tx", hash.Hex(), "account_nonce", accountNonce)
	return r.transition(ctx, c, Update{Status: StatusSubmitted, TxHash: &hash, RelayerNonce: &accountNonce})
}

// settleFromLog finds the tx that used this checkout's authorization (by
// nonce, and payer when known) and settles the checkout on it. It reports
// false when there is no such log yet.
func (r *laneReconciler) settleFromLog(ctx context.Context, c *Checkout, payer *common.Address) (bool, error) {
	from := c.CreatedBlock
	if from == 0 {
		head, err := r.chain.BlockNumber(ctx)
		if err != nil {
			return false, err
		}
		from = head - min(head, unknownBlockLookback)
	}
	log, err := r.chain.FindAuthorizationUsed(ctx, payer, c.Nonce, from)
	if err != nil || log == nil {
		return false, err
	}
	receipt, err := r.chain.Receipt(ctx, log.TxHash)
	if err != nil || receipt == nil {
		return false, err
	}
	if deep, err := r.deepEnough(ctx, receipt); err != nil || !deep {
		return true, err // found; wait for confirmations before deciding
	}
	// A tx we didn't send may reach the token through another contract, so
	// only the receipt status and the token's own logs are checked.
	return true, r.settle(ctx, c, log.Payer, log.TxHash, receipt)
}

// settle confirms the checkout if the receipt proves the payment. A pending
// checkout whose payment doesn't verify simply expires; any other fails.
func (r *laneReconciler) settle(ctx context.Context, c *Checkout, payer common.Address, hash common.Hash, receipt *types.Receipt) error {
	err := chain.VerifyTransfer(receipt, chain.ExpectedTransfer{
		Token: r.token, Payer: payer, Merchant: c.Merchant, Amount: c.AmountBaseUnits, Nonce: c.Nonce,
	})
	if err != nil {
		switch {
		case !c.Bound():
			return errUnverified // the caller looks on the other chains
		case c.Status == StatusPending:
			return r.transition(ctx, c, Update{Status: StatusExpired})
		}
		return r.fail(ctx, c, err.Error())
	}
	block := receipt.BlockNumber.Uint64()
	u := Update{Status: StatusConfirmed, Payer: &payer, TxHash: &hash, ConfirmedBlock: &block}
	if !c.Bound() {
		u.Bind = &Binding{ChainID: r.network.ChainID, Token: r.token, CreatedBlock: c.CreatedBlock}
	}
	slog.Info("reconciler: confirmed", "id", c.ID, "network", r.network.Key, "tx", hash.Hex(), "block", block)
	return r.transition(ctx, c, u)
}

func (r *laneReconciler) deepEnough(ctx context.Context, receipt *types.Receipt) (bool, error) {
	head, err := r.chain.BlockNumber(ctx)
	if err != nil {
		return false, err
	}
	mined := receipt.BlockNumber.Uint64()
	return head >= mined && head-mined+1 >= r.confirmations, nil
}

func (r *laneReconciler) fail(ctx context.Context, c *Checkout, reason string) error {
	slog.Warn("reconciler: failed", "id", c.ID, "reason", reason)
	return r.transition(ctx, c, Update{Status: StatusFailed, FailureReason: reason})
}

func (r *laneReconciler) transition(ctx context.Context, c *Checkout, u Update) error {
	u.At = r.now().UTC()
	ok, err := r.repo.Transition(ctx, c.ID, c.Status, u)
	if err != nil {
		return fmt.Errorf("storing %s → %s: %w", c.Status, u.Status, err)
	}
	if !ok {
		return fmt.Errorf("checkout left %s before %s could be stored", c.Status, u.Status)
	}
	return nil
}
