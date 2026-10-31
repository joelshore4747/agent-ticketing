-- The size rule counted a test line the same as a code line, so it punished testing (TP-212). Batch 3
-- was recorded at +603/-113 against 9a2134fe, of which +314/-3 was test files: 63% of its growth. A
-- builder who added the test that proved a change paid for it exactly as for code, so the cheapest way
-- under the ceiling was to thin the test — and it was already changing outcomes, TP-181 being closed
-- unmerged at net +90 of which +41 was its own two test files.
--
-- So the diff is recorded split. lines_added and lines_deleted keep meaning the whole diff, because
-- that is what every row written before this means; these two say how much of it was tests. The
-- ceiling reads the difference and the digest shows both, so growth in tests stays visible without
-- being charged. Null where nobody split it. The split is by path and nothing else, and
-- scripts/batch-size.sh holds the only pattern, so code moved into a test file to duck the ceiling
-- shows up in review rather than being absorbed by the figure.
ALTER TABLE batches
  ADD COLUMN test_lines_added   integer CHECK (test_lines_added   IS NULL OR test_lines_added   >= 0),
  ADD COLUMN test_lines_deleted integer CHECK (test_lines_deleted IS NULL OR test_lines_deleted >= 0),
  ADD CONSTRAINT batches_test_size_together
    CHECK ((test_lines_added IS NULL) = (test_lines_deleted IS NULL)),
  -- Test lines are part of the diff they were split out of, so a half larger than the whole is a bad
  -- post, not a big test suite.
  ADD CONSTRAINT batches_test_size_within_total CHECK (
    test_lines_added IS NULL
    OR (lines_added IS NOT NULL
        AND test_lines_added <= lines_added AND test_lines_deleted <= lines_deleted));
