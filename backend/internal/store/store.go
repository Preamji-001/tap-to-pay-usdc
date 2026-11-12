// Package store keeps checkouts in Postgres. It implements checkout.Repository.
package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and checks the connection.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate applies each embedded migrations/*.sql file once, in name order,
// recording it in schema_migrations. Running it again is a no-op.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		if err := s.applyMigration(ctx, name); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, name string) error {
	sql, err := migrationsFS.ReadFile(name)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Serialize concurrent migrators (e.g. two servers starting together).
	if _, err := tx.Exec(ctx, "LOCK TABLE schema_migrations IN EXCLUSIVE MODE"); err != nil {
		return err
	}
	var applied bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", name).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return nil
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const checkoutColumns = `id, amount_base_units::text, merchant, token, chain_id, nonce, valid_after, valid_before,
	merchant_reference, status, payer, signature, tx_hash, failure_reason, confirmed_block, relayer_nonce,
	created_at, expires_at, updated_at, created_block, start_blocks`

func (s *Store) Insert(ctx context.Context, c *checkout.Checkout) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO checkouts (
		id, amount_base_units, merchant, token, chain_id, nonce, valid_after, valid_before,
		merchant_reference, status, payer, signature, tx_hash, failure_reason, confirmed_block, relayer_nonce,
		created_at, expires_at, updated_at, created_block, start_blocks
	) VALUES ($1, $2::numeric, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`,
		c.ID, c.AmountBaseUnits.String(), c.Merchant.Bytes(), tokenBytes(c), chainID(c), c.Nonce[:],
		c.ValidAfter, c.ValidBefore, c.MerchantReference, string(c.Status),
		addressBytes(c.Payer), c.Signature, hashBytes(c.TxHash), c.FailureReason,
		uint64Ptr(c.ConfirmedBlock), uint64Ptr(c.RelayerNonce),
		c.CreatedAt, c.ExpiresAt, c.UpdatedAt, int64(c.CreatedBlock), encodeStartBlocks(c.StartBlocks),
	)
	return err
}

// An unbound checkout stores NULL for both its chain and its token.
func chainID(c *checkout.Checkout) *int64 {
	if !c.Bound() {
		return nil
	}
	return &c.ChainID
}

func tokenBytes(c *checkout.Checkout) []byte {
	if !c.Bound() {
		return nil
	}
	return c.Token.Bytes()
}

// start_blocks is JSON, and JSON object keys are strings.
func encodeStartBlocks(m map[int64]uint64) []byte {
	out := make(map[string]uint64, len(m))
	for id, b := range m {
		out[strconv.FormatInt(id, 10)] = b
	}
	b, _ := json.Marshal(out) // a map of numbers always encodes
	return b
}

func decodeStartBlocks(raw []byte) (map[int64]uint64, error) {
	var in map[string]uint64
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := make(map[int64]uint64, len(in))
	for k, b := range in {
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("start_blocks key %q: %w", k, err)
		}
		out[id] = b
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (*checkout.Checkout, error) {
	row := s.pool.QueryRow(ctx, "SELECT "+checkoutColumns+" FROM checkouts WHERE id = $1", id)
	c, err := scanCheckout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, checkout.ErrNotFound
	}
	return c, err
}

// MarkSubmitting is the one gate every authorization goes through. The
// WHERE clause makes the status change and the chain binding a single atomic
// step: of any number of racing authorizations, on any chains, exactly one
// row update happens. (SET expressions see the row as it was before the
// update, so created_block is only taken from b for a row being bound now.)
func (s *Store) MarkSubmitting(ctx context.Context, id string, b checkout.Binding, payer common.Address, sig []byte, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE checkouts SET
		status = 'submitting', payer = $2, signature = $3, updated_at = $4,
		created_block = CASE WHEN chain_id IS NULL THEN $7 ELSE created_block END,
		chain_id = $5, token = $6
		WHERE id = $1 AND status = 'pending' AND (chain_id IS NULL OR (chain_id = $5 AND token = $6))`,
		id, payer.Bytes(), sig, at, b.ChainID, b.Token.Bytes(), int64(b.CreatedBlock))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) Transition(ctx context.Context, id string, from checkout.Status, u checkout.Update) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE checkouts SET
		status = $3,
		payer = COALESCE($4, payer),
		tx_hash = COALESCE($5, tx_hash),
		failure_reason = CASE WHEN $6 = '' THEN failure_reason ELSE $6 END,
		confirmed_block = COALESCE($7, confirmed_block),
		relayer_nonce = COALESCE($8, relayer_nonce),
		updated_at = $9,
		chain_id = COALESCE($10, chain_id),
		token = COALESCE($11, token),
		created_block = COALESCE($12, created_block)
		WHERE id = $1 AND status = $2 AND ($10::bigint IS NULL OR chain_id IS NULL OR chain_id = $10)`,
		id, string(from), string(u.Status), addressBytes(u.Payer), hashBytes(u.TxHash), u.FailureReason,
		uint64Ptr(u.ConfirmedBlock), uint64Ptr(u.RelayerNonce), u.At, bindChain(u.Bind), bindToken(u.Bind), bindBlock(u.Bind))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func bindChain(b *checkout.Binding) *int64 {
	if b == nil {
		return nil
	}
	return &b.ChainID
}

func bindToken(b *checkout.Binding) []byte {
	if b == nil {
		return nil
	}
	return b.Token.Bytes()
}

func bindBlock(b *checkout.Binding) *int64 {
	if b == nil {
		return nil
	}
	v := int64(b.CreatedBlock)
	return &v
}

func (s *Store) ListActive(ctx context.Context) ([]*checkout.Checkout, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+checkoutColumns+
		" FROM checkouts WHERE status IN ('pending', 'submitting', 'submitted') ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*checkout.Checkout
	for rows.Next() {
		c, err := scanCheckout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCheckout(row pgx.Row) (*checkout.Checkout, error) {
	var (
		c                                     checkout.Checkout
		amount, status                        string
		merchant, token, nonce, payer, txHash []byte
		confirmedBlock, relayerNonce, chainID *int64
		createdBlock                          int64
		startBlocks                           []byte
	)
	err := row.Scan(&c.ID, &amount, &merchant, &token, &chainID, &nonce, &c.ValidAfter, &c.ValidBefore,
		&c.MerchantReference, &status, &payer, &c.Signature, &txHash, &c.FailureReason,
		&confirmedBlock, &relayerNonce, &c.CreatedAt, &c.ExpiresAt, &c.UpdatedAt, &createdBlock, &startBlocks)
	if err != nil {
		return nil, err
	}
	if chainID != nil {
		c.ChainID = *chainID
		c.Token = common.BytesToAddress(token)
	}
	if c.StartBlocks, err = decodeStartBlocks(startBlocks); err != nil {
		return nil, fmt.Errorf("checkout %s: %w", c.ID, err)
	}

	var ok bool
	if c.AmountBaseUnits, ok = new(big.Int).SetString(amount, 10); !ok {
		return nil, fmt.Errorf("checkout %s: bad amount %q", c.ID, amount)
	}
	c.Merchant = common.BytesToAddress(merchant)
	copy(c.Nonce[:], nonce)
	c.Status = checkout.Status(status)
	if payer != nil {
		a := common.BytesToAddress(payer)
		c.Payer = &a
	}
	if txHash != nil {
		h := common.BytesToHash(txHash)
		c.TxHash = &h
	}
	c.ConfirmedBlock = toUint64Ptr(confirmedBlock)
	c.RelayerNonce = toUint64Ptr(relayerNonce)
	c.CreatedBlock = uint64(createdBlock)
	c.CreatedAt, c.ExpiresAt, c.UpdatedAt = c.CreatedAt.UTC(), c.ExpiresAt.UTC(), c.UpdatedAt.UTC()
	return &c, nil
}

func addressBytes(a *common.Address) []byte {
	if a == nil {
		return nil
	}
	return a.Bytes()
}

func hashBytes(h *common.Hash) []byte {
	if h == nil {
		return nil
	}
	return h.Bytes()
}

func uint64Ptr(v *uint64) *int64 {
	if v == nil {
		return nil
	}
	i := int64(*v)
	return &i
}

func toUint64Ptr(v *int64) *uint64 {
	if v == nil {
		return nil
	}
	u := uint64(*v)
	return &u
}
