-- Inject high cache miss ratio: create a table larger than shared_buffers (32MB)
-- and perform a sequential scan, pushing the cache hit ratio down.

CREATE TABLE IF NOT EXISTS test_large_table (
    id SERIAL PRIMARY KEY,
    data TEXT
);

-- Insert ~50MB of data (well beyond 32MB shared_buffers).
INSERT INTO test_large_table (data)
SELECT repeat('y', 500) FROM generate_series(1, 100000);

-- Reset stats so the cache miss is clearly visible.
SELECT pg_stat_reset();

-- Force a sequential scan (no index on 'data' column).
SELECT count(*) FROM test_large_table WHERE data LIKE '%z%';
