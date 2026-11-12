# Stage 1: Backend Skeleton Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go service that creates checkouts (merchant API key) and serves their authoritative state, backed by Postgres, and refuses to start against the wrong chain or token.

**Architecture:** One binary `cmd/server`. Package `checkout` owns the domain types, rules and a `Repository` interface. `store` implements it on Postgres with embedded SQL migrations. `httpapi` exposes the endpoints. `chain` holds the EIP-712 domain separator and startup checks behind a small reader interface. `config` reads env vars, and `money` converts decimal strings to base units.

**Tech Stack:** Go 1.26, stdlib `net/http` (method+path patterns), `github.com/jackc/pgx/v5`, `github.com/ethereum/go-ethereum` (types, `crypto.Keccak256`, `ethclient`), Postgres 17 via `docker compose`.

**Spec:** `docs/superpowers/specs/2026-10-28-sponsored-checkout-design.md` (sections: Backend shape, Data model, States, API). Stage 1 = "Backend skeleton" in Stages. Deferred to later stages: the relayer keystore startup check and `CONFIRMATIONS` (Stage 3), and the EIP-712 typed data in GET plus the authorization endpoint (Stage 2).

**Owner's working style:** they are learning. Before each task, explain the concept in a few sentences (base units, opaque ids, server as source of truth, EIP-712 domain separator, migrations). Afterwards, give one hands-on thing to try. Pause after the stage for confirmation and a PR.

## Global Constraints

- Module path `github.com/Preamji-001/crypto-pos-base`; `go 1.26`.
- No float anywhere in amount handling; amounts are `*big.Int` base units, USDC `decimals = 6`.
- `CHAIN_ID=11155111`, `USDC_ADDRESS=0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238`, `USDC_EIP712_NAME=USDC`, `USDC_EIP712_VERSION=2`.
- Expected Sepolia domain separator `0xb90e5057db141a932946e64d09ccb7ffc9b00bd79fec26f698d29af0c83320a6`.
- `MERCHANT_ADDRESS=0x3CC845A0014aFcB0D52F673E0a98039074F1474c` (example value; real value only in `.env`).
- `CHECKOUT_TTL` default `15m`; `MAX_AMOUNT_BASE_UNITS` default `1000000000`; `HTTP_ADDR` default `:8080`.
- Checkout id = `co_` + base64url (no padding) of 16 random bytes (22 chars). Nonce = 32 random bytes from `crypto/rand`.
- Status strings exactly: `pending`, `submitting`, `submitted`, `confirmed`, `failed`, `expired`.
- Error JSON body: `{"error": "<human message>", "code": "<snake_case_code>"}`.
- `.env` is git-ignored; `.env.example` has placeholders only. No secrets in the repo or logs.
- Tests needing Postgres read `TEST_DATABASE_URL`, and `t.Skip` with a clear message when it's unset.
- Work on branch `stage-1-backend-skeleton` (from `milestone-1-design`). Commits end with the Co-Authored-By line.

## Review Focus

1. Odd amount strings (`"0"`, `"-1"`, `"1e3"`, `" 12.5"`, `"12."`, `".5"`, `"0.0000001"`, `"１２"`, a 100-digit number) must be rejected with 400 `invalid_amount`, never panic, never round. `"0012.50"` is accepted as 12500000.
2. A request body carrying `merchant`, `token`, `amount_base_units` or `chain_id` must be rejected (400 `invalid_request`), not silently ignored. That is the tampering guard at creation.
3. A checkout read at exactly `expires_at` reports `expired` (the boundary is `now >= expires_at`).
4. A malformed id in `GET /api/checkouts/{id}` (`../x`, 500 chars, empty) returns 404 `not_found`, never 500.
5. An empty or short `MERCHANT_API_KEY` (< 32 chars) makes startup fail, so the create endpoint can never be open to everyone.

---

### Task 1: Repo scaffolding and config

**Files:**
- Create: `go.mod`, `.gitignore` (`.env`, `/bin/`, `*.keystore`, `password*.txt`), `.env.example`, `compose.yaml`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  ```go
  type Config struct {
      HTTPAddr, DatabaseURL, RPCURL         string
      ChainID                               int64
      USDCAddress, MerchantAddress          common.Address
      USDCName, USDCVersion                 string
      MerchantAPIKey                        string
      FrontendBaseURL, CORSOrigin           string
      CheckoutTTL                           time.Duration
      MaxAmountBaseUnits                    *big.Int
  }
  func Load(getenv func(string) string) (Config, error)
  ```

- [ ] **Step 1:** Run `go mod init github.com/Preamji-001/crypto-pos-base` and set `go 1.26`. Write `compose.yaml` with a `postgres:17` service: user/password/db `pos`, host port **5433** → 5432 (Homebrew Postgres may already hold 5432), a named volume, and a healthcheck using `pg_isready`. Also add an init script creating a second database `pos_test`.
- [ ] **Step 2:** Write `.env.example` with every var from Config. Use `DATABASE_URL=postgres://pos:pos@localhost:5433/pos?sslmode=disable`, `TEST_DATABASE_URL=...pos_test...`, `SEPOLIA_RPC_URL=https://YOUR-SEPOLIA-RPC`, `MERCHANT_API_KEY=change-me-to-32+-random-chars`, `FRONTEND_BASE_URL=http://localhost:5173`, `CORS_ORIGIN=http://localhost:5173`, and the Global Constraints values.
- [ ] **Step 3: Write failing tests** in `config_test.go` with a map-backed `getenv`:
  - `TestLoadValid`: all vars set → no error; `ChainID == 11155111`; `CheckoutTTL == 15*time.Minute` when unset; `MaxAmountBaseUnits.String() == "1000000000"` when unset; `HTTPAddr == ":8080"` when unset.
  - `TestLoadRejects` (table): missing `DATABASE_URL`; missing `SEPOLIA_RPC_URL`; `USDC_ADDRESS="0x123"`; `MERCHANT_ADDRESS` = zero address; `MERCHANT_API_KEY` = `""` and = 31 chars; `CHAIN_ID="abc"`; `CHECKOUT_TTL="0s"`; `MAX_AMOUNT_BASE_UNITS="-5"`. Each → non-nil error that names the variable.
- [ ] **Step 4:** Run `go test ./internal/config/` → FAIL (undefined `Load`).
- [ ] **Step 5:** Implement `Load`. Use `common.IsHexAddress` before `common.HexToAddress`, and collect all errors with `errors.Join` so one run reports every bad variable.
- [ ] **Step 6:** Run `go test ./internal/config/` → PASS. Also run `docker compose up -d` and `docker compose ps`; expect a healthy `postgres`.
- [ ] **Step 7:** Commit: `feat: go module, config loading, local postgres`.

### Task 2: Amounts in base units

**Files:**
- Create: `internal/money/amount.go`
- Test: `internal/money/amount_test.go`

**Interfaces:**
- Produces:
  ```go
  var ErrInvalidAmount = errors.New("invalid amount")
  func ParseDecimal(s string, decimals uint8) (*big.Int, error) // wraps ErrInvalidAmount
  func FormatBaseUnits(v *big.Int, decimals uint8) string        // 12500000,6 → "12.5"
  ```

- [ ] **Step 1: Write failing tests:**
  - `TestParseDecimalValid` (decimals 6): `"12.50"→12500000`, `"1"→1000000`, `"0.000001"→1`, `"0012.50"→12500000`, `"999999999"→999999999000000`.
  - `TestParseDecimalInvalid`: `""`, `"0"`, `"0.000000"`, `"-1"`, `"+1"`, `"1e3"`, `" 12.5"`, `"12.5 "`, `"12."`, `".5"`, `"0.0000001"`, `"1,5"`, `"１２"`, `"0x10"`, and a 100-digit string. Each → `errors.Is(err, ErrInvalidAmount)`. (Upper bound is the caller's job, but cap input length at 40 chars here.)
  - `TestFormatBaseUnits`: `12500000→"12.5"`, `1→"0.000001"`, `1000000→"1"`, `0→"0"` (trailing zeros trimmed).
- [ ] **Step 2:** Run `go test ./internal/money/` → FAIL.
- [ ] **Step 3:** Implement. Parse with a regexp `^[0-9]+(\.[0-9]{1,N})?$` built from `decimals`, then build the integer from digits with `big.Int.SetString`. Reject zero.
- [ ] **Step 4:** Run `go test ./internal/money/` → PASS.
- [ ] **Step 5:** Commit: `feat: exact decimal to base-unit conversion`.

### Task 3: EIP-712 domain separator and startup token check

**Files:**
- Create: `internal/chain/domain.go`, `internal/chain/startup.go`
- Test: `internal/chain/domain_test.go`, `internal/chain/startup_test.go`, `internal/chain/startup_sepolia_test.go` (build tag `sepolia`)

**Interfaces:**
- Produces:
  ```go
  func DomainSeparator(name, version string, chainID int64, verifyingContract common.Address) common.Hash
  type Reader interface {
      ChainID(ctx context.Context) (*big.Int, error)
      CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
      CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
  }   // *ethclient.Client satisfies it
  type TokenParams struct { ChainID int64; Address common.Address; Name, Version string }
  func CheckToken(ctx context.Context, r Reader, p TokenParams) error
  ```

- [ ] **Step 1: Write failing tests:**
  - `TestDomainSeparatorSepoliaUSDC`: `DomainSeparator("USDC","2",11155111, 0x1c7D…7238)` == `0xb90e5057db141a932946e64d09ccb7ffc9b00bd79fec26f698d29af0c83320a6`.
  - `TestCheckToken` (table, fake Reader): all good → nil; RPC chain id 1 → error containing `"chain id"`; empty code → error containing `"no code"`; `DOMAIN_SEPARATOR()` returns a different 32 bytes → error containing `"domain separator"`; `ChainID` returns an error → that error is wrapped.
- [ ] **Step 2:** Run `go test ./internal/chain/` → FAIL.
- [ ] **Step 3:** Implement `DomainSeparator` as `keccak256(abi.encode(typeHash, keccak(name), keccak(version), chainId, contract))`. Use typeHash `keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")`, with 32-byte left-padded words. In `CheckToken`, call the `DOMAIN_SEPARATOR()` selector `0x3644e515` via `CallContract`.
- [ ] **Step 4:** Run `go test ./internal/chain/` → PASS.
- [ ] **Step 5:** Write `TestCheckTokenLiveSepolia` (tag `sepolia`, skipped unless `SEPOLIA_RPC_URL` is set). It dials `ethclient` and runs `CheckToken` with the real params, expecting nil. Run `go test -tags sepolia ./internal/chain/`. Report whether it actually ran.
- [ ] **Step 6:** Commit: `feat: EIP-712 domain separator and startup token check`.

### Task 4: Checkout domain and service

**Files:**
- Create: `internal/checkout/checkout.go` (types, statuses, errors), `internal/checkout/service.go`
- Test: `internal/checkout/service_test.go` (in-memory fake repository)

**Interfaces:**
- Consumes: `money.ParseDecimal`, `config.Config` fields.
- Produces:
  ```go
  type Status string // consts StatusPending … StatusExpired, values per Global Constraints
  type Checkout struct {
      ID string; AmountBaseUnits *big.Int; Merchant, Token common.Address; ChainID int64
      Nonce [32]byte; ValidAfter, ValidBefore int64; MerchantReference string; Status Status
      Payer *common.Address; Signature []byte; TxHash *common.Hash; FailureReason string
      ConfirmedBlock, RelayerNonce *uint64; CreatedAt, ExpiresAt, UpdatedAt time.Time
  }
  var ErrNotFound, ErrInvalidAmount, ErrAmountTooLarge, ErrInvalidReference error
  type Repository interface {
      Insert(ctx context.Context, c *Checkout) error
      Get(ctx context.Context, id string) (*Checkout, error) // ErrNotFound when missing
  }
  type Params struct { Merchant, Token common.Address; ChainID int64; TTL time.Duration; MaxAmount *big.Int; FrontendBaseURL string }
  type CreateRequest struct { Amount string; MerchantReference string }
  func NewService(repo Repository, p Params, now func() time.Time) *Service
  func (s *Service) Create(ctx context.Context, req CreateRequest) (*Checkout, error)
  func (s *Service) Get(ctx context.Context, id string) (*Checkout, error) // pending && now >= ExpiresAt → returned with Status=expired (no write in stage 1)
  func (s *Service) CheckoutURL(id string) string // FrontendBaseURL + "/checkout/" + id
  func ValidID(id string) bool // "co_" + 22 base64url chars
  ```

- [ ] **Step 1: Write failing tests** (fixed `now = 2026-10-28T10:00:00Z`):
  - `TestCreateSetsServerFields`: Amount `"12.50"` → `AmountBaseUnits == 12500000`, `Merchant`/`Token`/`ChainID` from Params, `Status == pending`, `ExpiresAt == now+15m`, `ValidAfter == 0`, `ValidBefore == ExpiresAt.Unix()`, `ValidID(ID)` true, nonce non-zero, stored in repo.
  - `TestCreateUniqueIDsAndNonces`: 1000 creates → no duplicate id or nonce.
  - `TestCreateRejects`: `"0"` → `ErrInvalidAmount`; `"1000.000001"` with max 1000 USDC → `ErrAmountTooLarge`; `"1000"` → ok (max is inclusive); reference of 65 chars or with a control char → `ErrInvalidReference`; reference `""` → ok.
  - `TestGetExpiry`: at `ExpiresAt - 1ns` → `pending`; exactly at `ExpiresAt` → `expired`; a `confirmed` checkout past expiry stays `confirmed`.
  - `TestGetNotFound`: unknown id → `ErrNotFound`; `ValidID` false for `""`, `"../x"`, a 500-char string, `"co_"+21 chars`.
  - `TestCheckoutURL`: base `http://localhost:5173` (and with a trailing `/`) → `http://localhost:5173/checkout/co_…`.
- [ ] **Step 2:** Run `go test ./internal/checkout/` → FAIL.
- [ ] **Step 3:** Implement. Reference rule: ≤ 64 bytes, printable ASCII only. `Get` returns `ErrNotFound` without touching the repo when `!ValidID(id)`.
- [ ] **Step 4:** Run `go test ./internal/checkout/` → PASS.
- [ ] **Step 5:** Commit: `feat: checkout domain and service`.

### Task 5: Postgres store and migrations

**Files:**
- Create: `internal/store/migrations/0001_checkouts.sql`, `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `checkout.Checkout`, `checkout.Repository`, `checkout.ErrNotFound`.
- Produces:
  ```go
  func Open(ctx context.Context, databaseURL string) (*Store, error) // pgxpool, pings
  func (s *Store) Migrate(ctx context.Context) error                 // idempotent
  func (s *Store) Close()
  // *Store implements checkout.Repository
  ```

- [ ] **Step 1:** Write the migration. Table `checkouts` with the spec's columns: `amount_base_units numeric(78,0) NOT NULL CHECK (> 0)`, `merchant`/`token`/`payer` `bytea` with `CHECK (octet_length = 20)`, `nonce bytea NOT NULL UNIQUE CHECK (octet_length = 32)`, `tx_hash bytea`, `status text NOT NULL CHECK (status IN (...six...))`, `relayer_nonce bigint`, `confirmed_block bigint`, `merchant_reference text NOT NULL DEFAULT ''`, timestamps as `timestamptz`, `valid_after`/`valid_before` `bigint`. Add an index on `status` for the later reconciler. `Migrate` keeps a `schema_migrations(version text PK, applied_at)` table and applies embedded `migrations/*.sql` in name order, each in its own transaction.
- [ ] **Step 2: Write failing tests** (skip without `TEST_DATABASE_URL`; each test truncates `checkouts`):
  - `TestMigrateIdempotent`: `Migrate` twice → no error.
  - `TestInsertGetRoundTrip`: insert a fully populated checkout (including a `*big.Int` above 2^64 to prove `numeric`) → `Get` returns an equal value (timestamps compared with `Equal`, UTC).
  - `TestGetMissing` → `errors.Is(err, checkout.ErrNotFound)`.
  - `TestDuplicateNonceRejected`: two checkouts with the same nonce → second `Insert` errors.
- [ ] **Step 3:** Run `TEST_DATABASE_URL=postgres://pos:pos@localhost:5433/pos_test?sslmode=disable go test ./internal/store/` → FAIL.
- [ ] **Step 4:** Implement with `pgxpool`. Convert `*big.Int` ↔ `numeric` via `pgtype.Numeric` (or text cast), and addresses as 20 raw bytes.
- [ ] **Step 5:** Run the same command → PASS (and confirm in the output that the tests ran, not skipped).
- [ ] **Step 6:** Commit: `feat: postgres store with embedded migrations`.

### Task 6: HTTP API and server wiring

**Files:**
- Create: `internal/httpapi/server.go` (routes, middleware), `internal/httpapi/checkouts.go` (handlers, JSON shapes)
- Create: `cmd/server/main.go`
- Create: `README.md` (short: what it is, `docker compose up -d`, how to run, curl examples; the full README is Stage 4)
- Test: `internal/httpapi/checkouts_test.go` (`httptest`, real `checkout.Service` over an in-memory fake repo)

**Interfaces:**
- Consumes: `checkout.Service`, `config.Config`, `store.Open/Migrate`, `chain.CheckToken`, `money.FormatBaseUnits`.
- Produces:
  ```go
  func NewServer(svc *checkout.Service, apiKey, corsOrigin string) http.Handler
  ```
  Routes: `POST /api/checkouts`, `GET /api/checkouts/{id}`, `GET /healthz`, plus `OPTIONS` preflight for `/api/` paths. The response JSON for a checkout (used by Stage 2's frontend):
  ```json
  {"id":"co_…","checkout_url":"http://localhost:5173/checkout/co_…","status":"pending",
   "amount":"12.5","amount_base_units":"12500000",
   "token":{"symbol":"USDC","address":"0x1c7D…7238","decimals":6},
   "merchant":"0x3CC8…474c","chain_id":11155111,"merchant_reference":"ORDER-1",
   "created_at":"2026-10-28T10:00:00Z","expires_at":"2026-10-28T10:15:00Z","tx_hash":null}
  ```
  Addresses use EIP-55 checksum form, and times are RFC 3339 UTC.

- [ ] **Step 1: Write failing tests:**
  - `TestCreateRequiresAPIKey`: no header → 401 `unauthorized`; wrong key → 401; correct `X-API-Key` → 201 with the JSON shape above, `Location: /api/checkouts/{id}`.
  - `TestCreateRejectsTampering` (table): bodies with `"merchant"`, `"token"`, `"amount_base_units"`, `"chain_id"`, or `"status"` fields → 400 `invalid_request`; non-JSON → 400; body > 4 KiB → 400 (or 413).
  - `TestCreateInvalidAmount`: `{"amount":"1e3"}` → 400 `invalid_amount`; over max → 400 `amount_too_large`; `{"amount":12.5}` (a JSON number, not a string) → 400 `invalid_request`.
  - `TestGetCheckout`: create then GET → 200 with the same body; unknown valid-shaped id → 404 `not_found`; `GET /api/checkouts/..%2Fx` and a 500-char id → 404; GET needs **no** API key.
  - `TestCORS`: `OPTIONS /api/checkouts/x` with `Origin: http://localhost:5173` → `Access-Control-Allow-Origin` echoes it; another origin → no header.
  - `TestInternalErrorHidesDetails`: a fake repo returning `errors.New("pg: secret detail")` → 500 `internal`, and the body doesn't contain `"secret"`.
- [ ] **Step 2:** Run `go test ./internal/httpapi/` → FAIL.
- [ ] **Step 3:** Implement. Decode with `DisallowUnknownFields`, `http.MaxBytesReader(4096)`, a request struct with `Amount string` and `MerchantReference string` (JSON `merchant_reference`). Compare API keys with `subtle.ConstantTimeCompare`. Map service errors to codes. Log internal errors server-side with `log/slog` and don't put request bodies in logs.
- [ ] **Step 4:** Run `go test ./internal/httpapi/` → PASS.
- [ ] **Step 5:** Implement `main.go`. The startup order is `config.Load(os.Getenv)` → `ethclient.DialContext` → `chain.CheckToken` (exit non-zero with the message on failure) → `store.Open` → `Migrate` → `http.Server` with read/write timeouts. Shut down gracefully on SIGINT/SIGTERM.
- [ ] **Step 6: Full verification.** Run `gofmt -l .` (expect no output), `go vet ./...`, and `go test ./...` with `TEST_DATABASE_URL` set, then `go test -tags sepolia ./internal/chain/` if an RPC is available. All pass; note any that skipped.
- [ ] **Step 7: Manual run.** `cp .env.example .env`, then fill in the RPC URL and a random API key (`openssl rand -hex 32`). Run `set -a; . ./.env; set +a; go run ./cmd/server` and expect a log line that the token check passed. Then:
  `curl -s -XPOST localhost:8080/api/checkouts -H "X-API-Key: $MERCHANT_API_KEY" -d '{"amount":"1.25","merchant_reference":"ORDER-1"}'` → 201. Then `curl -s localhost:8080/api/checkouts/<id>` → the same state. Also try a wrong `CHAIN_ID=1` and confirm the server refuses to start.
- [ ] **Step 8:** Commit: `feat: checkout HTTP API and server wiring`. Then push the branch, create the private GitHub repo `Preamji-001/crypto-pos-base` (ask the owner first; it's outward-facing) and open the Stage 1 PR.
