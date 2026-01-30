-- Clean up high cache miss test.
DROP TABLE IF EXISTS test_large_table;
SELECT pg_stat_reset();
