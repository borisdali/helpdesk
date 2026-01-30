-- Inject table bloat: insert many rows, delete most, disable autovacuum.
-- This creates a large number of dead tuples visible in pg_stat_user_tables.

CREATE TABLE IF NOT EXISTS test_bloat_table (
    id SERIAL PRIMARY KEY,
    data TEXT
);

-- Disable autovacuum on this table so dead tuples accumulate.
ALTER TABLE test_bloat_table SET (autovacuum_enabled = false);

-- Insert 100k rows.
INSERT INTO test_bloat_table (data)
SELECT repeat('x', 200) FROM generate_series(1, 100000);

-- Delete 90% of them, leaving dead tuples.
DELETE FROM test_bloat_table WHERE id % 10 != 0;

-- Force a checkpoint so stats are visible.
CHECKPOINT;
