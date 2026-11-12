-- The chain head when the checkout was created: the lower bound for log
-- searches. 0 for rows created before this migration (unknown).
ALTER TABLE checkouts ADD COLUMN created_block bigint NOT NULL DEFAULT 0;
