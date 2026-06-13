--
-- aiHelpDesk fault injection helper script.
--
-- Inject high cache miss ratio: reset pg_stat_database counters, then force a
-- sequential scan so that blks_read dominates blks_hit, depressing the cache
-- hit ratio below 95%.

CREATE TABLE IF NOT EXISTS test_large_table (
    id SERIAL PRIMARY KEY,
    data TEXT
);

-- Insert ~50MB of data to ensure the sequential scan generates meaningful I/O.
INSERT INTO test_large_table (data)
SELECT repeat('y', 500) FROM generate_series(1, 100000);

-- Reset stats so the cache miss is clearly visible.
SELECT pg_stat_reset();

-- Force a sequential scan (no index on 'data' column).
SELECT count(*) FROM test_large_table WHERE data LIKE '%z%';
