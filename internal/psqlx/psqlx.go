// Package psqlx parses `psql -x` (expanded/vertical) output into structured
// records, so callers that already shell out to psql (see
// agents/database/tools.go's runPsqlAs) can get typed fields out of the same
// text they already capture for the LLM, without switching to a native SQL
// driver.
//
// Format, confirmed against a real psql 16 instance (not assumed):
//
//	-[ RECORD 1 ]---+--------------------
//	pid             | 31
//	user            | postgres
//	-[ RECORD 2 ]---+--------------------
//	pid             | 32
//	user            |
//
// A `psql -x -c "STMT1; STMT2; STMT3"` invocation prints each statement's
// own result set separately, delimited by a single blank line between
// statements — record boundaries *within* one statement's result set have
// no blank line between them. A statement returning zero rows prints a
// literal "(0 rows)" line in place of any record blocks. The trailing
// decoration after "-[ RECORD N ]" (dashes/+ for column-width padding) is
// variable — sometimes absent entirely for narrow output — so it's matched
// by prefix only, never depended on for content.
package psqlx

import (
	"regexp"
	"strings"
)

// Record is one row's fields, keyed by column name, values as raw strings
// exactly as psql printed them (no type conversion — callers know their own
// column types).
type Record map[string]string

var recordHeaderRe = regexp.MustCompile(`^-\[ RECORD \d+ \]`)

// ParseExpanded parses the output of a `psql -x` invocation into one
// []Record per SQL statement, in statement order. A statement that
// returned zero rows contributes an empty, non-nil []Record — callers for
// whom "this row is absent" is itself a meaningful signal (e.g. a
// disconnected replica no longer appearing in pg_stat_replication) should
// treat len(result[i]) == 0 as exactly that, not as a parse failure.
func ParseExpanded(output string) [][]Record {
	var statements [][]Record
	var current []Record
	var currentRecord Record
	sawLineThisStatement := false

	flushRecord := func() {
		if currentRecord != nil {
			current = append(current, currentRecord)
			currentRecord = nil
		}
	}
	flushStatement := func() {
		flushRecord()
		if sawLineThisStatement {
			if current == nil {
				current = []Record{}
			}
			statements = append(statements, current)
		}
		current = nil
		sawLineThisStatement = false
	}

	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			flushStatement()
			continue
		}
		sawLineThisStatement = true

		if recordHeaderRe.MatchString(line) {
			flushRecord()
			currentRecord = Record{}
			continue
		}
		if strings.TrimSpace(line) == "(0 rows)" {
			continue // zero-row marker, not a field line
		}
		if currentRecord == nil {
			continue // stray line outside any record — defensive, shouldn't happen
		}
		idx := strings.Index(line, "|")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		currentRecord[key] = val
	}
	flushStatement()
	return statements
}
