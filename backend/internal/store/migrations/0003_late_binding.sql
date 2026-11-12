-- L4: a checkout's chain is bound when the customer authorizes, not at
-- creation. Until then chain_id and token are NULL.
ALTER TABLE checkouts ALTER COLUMN chain_id DROP NOT NULL;
ALTER TABLE checkouts ALTER COLUMN token DROP NOT NULL;
-- The chain and its USDC are set together or not at all.
ALTER TABLE checkouts ADD CONSTRAINT checkouts_chain_and_token_together
    CHECK ((chain_id IS NULL) = (token IS NULL));
-- Only a checkout nobody paid can stay unbound: anything sent, confirmed or
-- failed happened on one chain.
ALTER TABLE checkouts ADD CONSTRAINT checkouts_unbound_only_unpaid
    CHECK (chain_id IS NOT NULL OR status IN ('pending', 'expired'));
-- Every enabled chain's head at creation, as {"84532": 1234, ...}: where a
-- search for this checkout's payment starts, whichever chain it lands on.
ALTER TABLE checkouts ADD COLUMN start_blocks jsonb NOT NULL DEFAULT '{}';
UPDATE checkouts SET start_blocks = jsonb_build_object(chain_id::text, created_block)
    WHERE created_block > 0;
