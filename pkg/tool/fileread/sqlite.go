package fileread

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/liuy/gbot/pkg/tool"
)

// SQLite magic header: "SQLite format 3\0"
var sqliteMagic = []byte("SQLite format 3\x00")

// SQLITE_PATH_PATTERN matches .sqlite/.sqlite3/.db/.db3 followed by :, ?, or end.
// omp: /\.(?:sqlite3?|db3?)(?=(?::|\?|$))/gi
var sqlitePathPattern = regexp.MustCompile(`\.(?:sqlite3?|db3?)(?::|\?|$)`)

// Query constants
const (
	sqliteDefaultQueryLimit         = 20
	sqliteDefaultSchemaSample       = 5
	sqliteMaxQueryLimit             = 500
	sqliteMaxRawQueryRows           = 1000
	sqliteRowCountProbeCap    int64 = 50000
)

// sqlitePathCandidate represents a parsed SQLite path.
type sqlitePathCandidate struct {
	sqlitePath  string
	subPath     string
	queryString string
}

// sqliteSelector is the parsed selector from subpath + querystring.
type sqliteSelector struct {
	kind        sqliteSelectorKind
	table       string
	key         string
	limit       int
	offset      int
	order       string
	where       string
	rawSQL      string
	sampleLimit int
}

type sqliteSelectorKind int

const (
	sqliteSelList sqliteSelectorKind = iota
	sqliteSelSchema
	sqliteSelRow
	sqliteSelQuery
	sqliteSelRaw
)

// looksLikeSqlite checks the first 16 bytes for the SQLite magic header.
func looksLikeSqlite(data []byte) bool {
	if len(data) < len(sqliteMagic) {
		return false
	}
	return bytesEqual(data[:len(sqliteMagic)], sqliteMagic)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// isSqliteFile reads the first 16 bytes and checks the magic header.
func isSqliteFile(absPath string) bool {
	f, err := os.Open(absPath)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 16)
	n, err := f.Read(header)
	if err != nil || n < 16 {
		return false
	}
	return looksLikeSqlite(header)
}

// parseSqlitePathCandidates finds all possible SQLite path splits in the input.
// omp: parseSqlitePathCandidates — finds .sqlite/.db extensions followed by : or ?
func parseSqlitePathCandidates(filePath string) []sqlitePathCandidate {
	normalized := strings.ReplaceAll(filePath, "\\", "/")
	seen := map[string]bool{}
	var candidates []sqlitePathCandidate

	matches := sqlitePathPattern.FindAllStringIndex(normalized, -1)
	for _, match := range matches {
		start := match[0]
		// match end is the position after the extension
		matchText := normalized[start:match[1]]
		// Find the actual extension end — pattern includes the trailing : ? or $
		// We need the path up to but not including the separator
		extEnd := start + len(matchText)
		// The regex includes the separator char in the match; adjust
		lastChar := matchText[len(matchText)-1]
		if lastChar == ':' || lastChar == '?' {
			extEnd--
		}
		sqlitePath := filePath[:extEnd]
		remainder := normalized[extEnd:]

		subPath, queryString := splitSqliteRemainder(remainder)
		key := sqlitePath + "\x00" + subPath + "\x00" + queryString
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, sqlitePathCandidate{
			sqlitePath:  sqlitePath,
			subPath:     subPath,
			queryString: queryString,
		})
	}

	// Sort by sqlitePath length descending — prefer longer (more specific) paths
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if len(candidates[j].sqlitePath) > len(candidates[i].sqlitePath) {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
	return candidates
}

// splitSqliteRemainder splits "remainder" into subpath and querystring parts.
// omp: splitSqliteRemainder
func splitSqliteRemainder(remainder string) (subPath, queryString string) {
	before, after, ok := strings.Cut(remainder, "?")
	if !ok {
		return strings.TrimLeft(remainder, ":"), ""
	}
	return strings.TrimLeft(before, ":"), after
}

// parseSqliteSelector parses subpath + querystring into a structured selector.
// omp: parseSqliteSelector
func parseSqliteSelector(subPath, queryString string) (*sqliteSelector, error) {
	normalizedSubPath := strings.TrimLeft(strings.TrimSpace(subPath), ":")
	params := parseQueryString(queryString)
	rawQuery := params["q"]

	if rawQuery != "" {
		if normalizedSubPath != "" || len(params) > 1 {
			return nil, fmt.Errorf("sqlite raw queries cannot be combined with table selectors or pagination")
		}
		if strings.TrimSpace(rawQuery) == "" {
			return nil, fmt.Errorf("sqlite query parameter 'q' cannot be empty")
		}
		return &sqliteSelector{kind: sqliteSelRaw, rawSQL: rawQuery}, nil
	}

	if normalizedSubPath == "" {
		if len(params) > 0 {
			return nil, fmt.Errorf("sqlite query parameters require a table selector or q=SELECT")
		}
		return &sqliteSelector{kind: sqliteSelList}, nil
	}

	// Split table:key
	before, after, ok := strings.Cut(normalizedSubPath, ":")
	var table, key string
	if !ok {
		table = normalizedSubPath
	} else {
		table = before
		key = after
	}
	if table == "" {
		return nil, fmt.Errorf("sqlite selectors must include a table name")
	}

	if key != "" {
		if len(params) > 0 {
			return nil, fmt.Errorf("sqlite row lookups cannot be combined with query parameters")
		}
		return &sqliteSelector{kind: sqliteSelRow, table: table, key: key}, nil
	}

	hasQueryParams := params["limit"] != "" || params["offset"] != "" || params["order"] != "" || params["where"] != ""
	if hasQueryParams {
		for k := range params {
			if k != "limit" && k != "offset" && k != "order" && k != "where" {
				return nil, fmt.Errorf("unsupported SQLite query parameter '%s'", k)
			}
		}
		limit, err := parseSqliteLimit(params["limit"], sqliteDefaultQueryLimit)
		if err != nil {
			return nil, err
		}
		offset, err := parseSqliteOffset(params["offset"])
		if err != nil {
			return nil, err
		}
		where := params["where"]
		if err := validateWhereClause(where); err != nil {
			return nil, err
		}
		return &sqliteSelector{
			kind:   sqliteSelQuery,
			table:  table,
			limit:  limit,
			offset: offset,
			order:  params["order"],
			where:  where,
		}, nil
	}

	if len(params) > 0 {
		for k := range params {
			return nil, fmt.Errorf("unsupported SQLite query parameter '%s'", k)
		}
	}

	return &sqliteSelector{kind: sqliteSelSchema, table: table, sampleLimit: sqliteDefaultSchemaSample}, nil
}

// parseQueryString parses "key=value&key2=value2" into a map.
func parseQueryString(qs string) map[string]string {
	result := map[string]string{}
	if qs == "" {
		return result
	}
	for pair := range strings.SplitSeq(qs, "&") {
		if pair == "" {
			continue
		}
		before, after, ok := strings.Cut(pair, "=")
		if !ok {
			result[pair] = ""
		} else {
			result[before] = after
		}
	}
	return result
}

func parseSqliteLimit(value string, fallback int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("sqlite limit must be a positive integer; got '%s'", value)
	}
	if parsed > sqliteMaxQueryLimit {
		return sqliteMaxQueryLimit, nil
	}
	return parsed, nil
}

func parseSqliteOffset(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("sqlite offset must be a non-negative integer; got '%s'", value)
	}
	return parsed, nil
}

var forbiddenWhereKeywords = map[string]bool{
	"limit": true, "offset": true, "union": true,
	"intersect": true, "except": true,
	"attach": true, "detach": true, "pragma": true,
}

// validateWhereClause rejects SQL control syntax in where clauses.
// omp: findWhereClauseViolation
func validateWhereClause(where string) error {
	if strings.TrimSpace(where) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(where)

	inSingle := false
	inDouble := false
	tokenStart := -1
	flushToken := func(end int) error {
		if tokenStart < 0 {
			return nil
		}
		token := strings.ToLower(trimmed[tokenStart:end])
		tokenStart = -1
		if forbiddenWhereKeywords[token] {
			return fmt.Errorf("%s", forbiddenWhereKeywordError)
		}
		return nil
	}

	for i := 0; i <= len(trimmed); i++ {
		var ch byte
		if i < len(trimmed) {
			ch = trimmed[i]
		}

		if inSingle {
			if ch == '\'' && i+1 < len(trimmed) && trimmed[i+1] == '\'' {
				i++
				continue
			}
			if ch == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			if ch == '"' && i+1 < len(trimmed) && trimmed[i+1] == '"' {
				i++
				continue
			}
			if ch == '"' {
				inDouble = false
			}
			continue
		}

		isIdent := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_'
		if isIdent {
			if tokenStart < 0 {
				tokenStart = i
			}
			continue
		}

		if err := flushToken(i); err != nil {
			return err
		}

		if i >= len(trimmed) {
			break
		}
		if ch == '\'' {
			inSingle = true
			continue
		}
		if ch == '"' {
			inDouble = true
			continue
		}
		if ch == ';' {
			return fmt.Errorf("sqlite 'where' clause must not contain comments or statement terminators; use '?q=SELECT ...' for raw SQL")
		}
		if (ch == '-' && i+1 < len(trimmed) && trimmed[i+1] == '-') ||
			(ch == '/' && i+1 < len(trimmed) && trimmed[i+1] == '*') {
			return fmt.Errorf("sqlite 'where' clause must not contain comments or statement terminators; use '?q=SELECT ...' for raw SQL")
		}
	}

	return nil
}

const forbiddenWhereKeywordError = "sqlite 'where' clause must not contain LIMIT/OFFSET/UNION/INTERSECT/EXCEPT/ATTACH/DETACH/PRAGMA; use '?q=SELECT ...' for raw SQL"

// quoteIdent quotes a SQLite identifier with double quotes.
func quoteIdent(ident string) string {
	return "\"" + strings.ReplaceAll(ident, "\"", "\"\"") + "\""
}

// executeSqliteRead handles SQLite database reads with selector syntax.
// omp: #readSqlite
func executeSqliteRead(ctx context.Context, in Input, candidate sqlitePathCandidate, absPath string) (*tool.ToolResult, error) {
	selector, err := parseSqliteSelector(candidate.subPath, candidate.queryString)
	if err != nil {
		return nil, err
	}

	// Open read-only: mode=ro prevents accidental writes
	dsn := "file:" + absPath + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	defer db.Close()

	// Set busy timeout to match omp (3 seconds)
	_, _ = db.Exec("PRAGMA busy_timeout = 3000")

	var content string
	switch selector.kind {
	case sqliteSelList:
		content, err = sqliteListTables(db)
	case sqliteSelSchema:
		content, err = sqliteGetSchema(db, selector.table, selector.sampleLimit)
	case sqliteSelRow:
		content, err = sqliteGetRowByKey(db, selector.table, selector.key)
	case sqliteSelQuery:
		content, err = sqliteQueryRows(db, selector)
	case sqliteSelRaw:
		content, err = sqliteExecuteRawQuery(db, selector.rawSQL)
	}
	if err != nil {
		return nil, err
	}

	return &tool.ToolResult{Data: TextOutput{
		Type:     "text",
		FilePath: in.FilePath,
		Content:  content,
	}}, nil
}

// sqliteListTables lists all tables with row counts.
func sqliteListTables(db *sql.DB) (string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name COLLATE NOCASE")
	if err != nil {
		return "", fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		names = append(names, name)
	}

	if len(names) == 0 {
		return "(no tables)", nil
	}

	var lines []string
	for _, name := range names {
		count := probeRowCount(db, name)
		lines = append(lines, fmt.Sprintf("%s (%s)", name, formatRowCount(count)))
	}
	return strings.Join(lines, "\n"), nil
}

// tableRowCount represents a row count result.
type tableRowCount struct {
	kind string // "exact", "estimate", "atLeast"
	rows int64
}

func probeRowCount(db *sql.DB, table string) tableRowCount {
	// Try sqlite_stat1 estimate first
	estimates := loadRowEstimates(db)
	if est, ok := estimates[table]; ok && est > sqliteRowCountProbeCap {
		return tableRowCount{kind: "estimate", rows: est}
	}

	// Count exactly, capped
	var counted int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s LIMIT %d)", quoteIdent(table), sqliteRowCountProbeCap+1)
	err := db.QueryRow(query).Scan(&counted)
	if err != nil {
		return tableRowCount{kind: "exact", rows: 0}
	}
	if counted > sqliteRowCountProbeCap {
		return tableRowCount{kind: "atLeast", rows: sqliteRowCountProbeCap}
	}
	return tableRowCount{kind: "exact", rows: counted}
}

func loadRowEstimates(db *sql.DB) map[string]int64 {
	estimates := map[string]int64{}
	var hasStat1 string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_stat1' LIMIT 1").Scan(&hasStat1)
	if err != nil {
		return estimates
	}

	rows, err := db.Query("SELECT tbl, stat FROM sqlite_stat1")
	if err != nil {
		return estimates
	}
	defer rows.Close()

	for rows.Next() {
		var tbl string
		var stat sql.NullString
		if err := rows.Scan(&tbl, &stat); err != nil {
			continue
		}
		if !stat.Valid || stat.String == "" {
			continue
		}
		// First integer in stat is the row count for that index
		fields := strings.Fields(stat.String)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if prev, ok := estimates[tbl]; !ok || n > prev {
			estimates[tbl] = n
		}
	}
	return estimates
}

func formatRowCount(c tableRowCount) string {
	switch c.kind {
	case "exact":
		return fmt.Sprintf("%d rows", c.rows)
	case "estimate":
		return fmt.Sprintf("~%d rows", c.rows)
	case "atLeast":
		return fmt.Sprintf("%d+ rows", c.rows)
	}
	return ""
}

// sqliteGetSchema returns CREATE TABLE SQL + sample rows.
func sqliteGetSchema(db *sql.DB, table string, sampleLimit int) (string, error) {
	var createSQL string
	err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name = ?", table).Scan(&createSQL)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("sqlite table '%s' not found", table)
	}
	if err != nil {
		return "", err
	}

	// Sample rows
	columns, rows, _, err := sqliteSelectRows(db, table, sampleLimit, 0, "", "")
	if err != nil {
		return createSQL, nil // schema alone if sample fails
	}

	parts := []string{createSQL, "", "Sample rows:", buildSqliteOutput(columns, rows)}
	return strings.Join(parts, "\n"), nil
}

// sqliteGetRowByKey finds a single row by primary key or rowid.
// For string PKs, if no exact match is found, falls back to prefix match:
// returns the row if exactly one PK starts with the key, otherwise hints the user
// to provide a longer prefix.
func sqliteGetRowByKey(db *sql.DB, table, key string) (string, error) {
	pkCol, pkType, err := getTablePrimaryKey(db, table)
	if err != nil {
		return "", err
	}

	if pkCol == "" {
		// Fall back to rowid — integer only, no prefix
		rowID, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return "", fmt.Errorf("sqlite ROWID must be an integer; got '%s'", key)
		}
		query := fmt.Sprintf("SELECT * FROM %s WHERE rowid = ? LIMIT 1", quoteIdent(table))
		rendered, err := scanSingleRow(db, query, []any{rowID})
		if err != nil {
			return "", err
		}
		if rendered == "" {
			return fmt.Sprintf("No row found in table '%s' for rowid '%s'.", table, key), nil
		}
		return rendered, nil
	}

	// Exact match
	arg, err := coerceLookupValue(key, pkType)
	if err != nil {
		return "", err
	}
	query := fmt.Sprintf("SELECT * FROM %s WHERE %s = ? LIMIT 1", quoteIdent(table), quoteIdent(pkCol))
	rendered, err := scanSingleRow(db, query, []any{arg})
	if err != nil {
		return "", err
	}
	if rendered != "" {
		return rendered, nil
	}

	// Prefix match fallback for string/text PKs only.
	// Integer PKs are skipped because '12' could spuriously prefix-match 1234.
	if isTextPK(pkType) {
		prefixQuery := fmt.Sprintf("SELECT * FROM %s WHERE %s LIKE ? ESCAPE '\\' LIMIT 2", quoteIdent(table), quoteIdent(pkCol))
		prefix, err := escapeLikePattern(key)
		if err != nil {
			return "", err
		}
		rendered, matchedCount, err := scanRowsWithCount(db, prefixQuery, []any{prefix + "%"})
		if err != nil {
			return "", err
		}
		if matchedCount == 1 {
			return rendered, nil
		}
		if matchedCount > 1 {
			return fmt.Sprintf("Multiple rows in '%s' have PK starting with '%s'; provide a longer prefix.", table, key), nil
		}
	}

	return fmt.Sprintf("No row found in table '%s' for key '%s'.", table, key), nil
}

// isTextPK returns true for string-like PK types where prefix matching is safe.
func isTextPK(typ string) bool {
	upper := strings.ToUpper(typ)
	return strings.Contains(upper, "TEXT") || strings.Contains(upper, "CHAR") || strings.Contains(upper, "CLOB")
}

// escapeLikePattern escapes % _ \ in a LIKE pattern operand.
func escapeLikePattern(s string) (string, error) {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}

// scanSingleRow runs a query expected to return 0 or 1 row and renders it.
// Returns empty string if no row matched.
func scanSingleRow(db *sql.DB, query string, args []any) (string, error) {
	qRows, err := db.Query(query, args...)
	if err != nil {
		return "", err
	}
	defer qRows.Close()

	colNames, err := qRows.Columns()
	if err != nil {
		return "", err
	}
	if !qRows.Next() {
		return "", nil
	}
	vals := make([]any, len(colNames))
	ptrs := make([]any, len(colNames))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := qRows.Scan(ptrs...); err != nil {
		return "", err
	}
	var lines []string
	for i, col := range colNames {
		lines = append(lines, fmt.Sprintf("%s: %s", col, stringifySqliteValue(vals[i])))
	}
	return strings.Join(lines, "\n"), nil
}

// scanRowsWithCount runs a LIMIT-bounded query, returns the first row rendered
// (if any) and the total number of rows matched (up to LIMIT). Caller uses count
// to disambiguate between single and multiple matches.
func scanRowsWithCount(db *sql.DB, query string, args []any) (string, int, error) {
	qRows, err := db.Query(query, args...)
	if err != nil {
		return "", 0, err
	}
	defer qRows.Close()

	colNames, err := qRows.Columns()
	if err != nil {
		return "", 0, err
	}
	count := 0
	var firstRendered string
	for qRows.Next() {
		count++
		vals := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := qRows.Scan(ptrs...); err != nil {
			return "", 0, err
		}
		if count == 1 {
			var lines []string
			for i, col := range colNames {
				lines = append(lines, fmt.Sprintf("%s: %s", col, stringifySqliteValue(vals[i])))
			}
			firstRendered = strings.Join(lines, "\n")
		}
	}
	return firstRendered, count, nil
}

// sqliteQueryRows paginated query with optional where/order.
func sqliteQueryRows(db *sql.DB, sel *sqliteSelector) (string, error) {
	columns, rows, totalCount, err := sqliteSelectRows(db, sel.table, sel.limit, sel.offset, sel.where, sel.order)
	if err != nil {
		return "", err
	}

	table := buildSqliteOutput(columns, rows)
	shown := sel.offset + len(rows)
	if shown < totalCount {
		remaining := totalCount - shown
		nextOffset := sel.offset + len(rows)
		table += fmt.Sprintf("\n[%d more rows; append :%s?limit=%d&offset=%d to the database path to continue]",
			remaining, sel.table, sel.limit, nextOffset)
	}
	return table, nil
}

// sqliteSelectRows executes a SELECT with limit/offset/where/order.
func sqliteSelectRows(db *sql.DB, table string, limit, offset int, where, order string) ([]string, []map[string]any, int, error) {
	// Validate where
	if err := validateWhereClause(where); err != nil {
		return nil, nil, 0, err
	}

	whereClause := ""
	if where != "" {
		whereClause = " WHERE " + where
	}

	// Get column names for order validation
	cols, err := getTableColumns(db, table)
	if err != nil {
		return nil, nil, 0, err
	}

	orderClause := ""
	if order != "" {
		oc, err := resolveOrderClause(order, cols)
		if err != nil {
			return nil, nil, 0, err
		}
		orderClause = oc
	}

	// Total count
	var totalCount int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", quoteIdent(table), whereClause)
	if err := db.QueryRow(countSQL).Scan(&totalCount); err != nil {
		return nil, nil, 0, err
	}

	// Select
	selectSQL := fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d",
		quoteIdent(table), whereClause, orderClause, limit, offset)
	qRows, err := db.Query(selectSQL)
	if err != nil {
		return nil, nil, 0, err
	}
	defer qRows.Close()

	colNames, err := qRows.Columns()
	if err != nil {
		return nil, nil, 0, err
	}

	var resultRows []map[string]any
	for qRows.Next() {
		vals := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := qRows.Scan(ptrs...); err != nil {
			return nil, nil, 0, err
		}
		row := map[string]any{}
		for i, col := range colNames {
			row[col] = vals[i]
		}
		resultRows = append(resultRows, row)
	}

	return colNames, resultRows, totalCount, nil
}

// sqlHasExplicitLimit reports whether the SQL text contains a LIMIT clause
// with a numeric bound. Used to decide whether the sqliteMaxRawQueryRows cap
// should be bypassed (user explicitly opted into more rows).
//
// Strips single-quoted string literals before scanning so the word "limit"
// inside a string doesn't trigger a false positive. Requires LIMIT to be
// followed by an integer to avoid matching column names like `SELECT limit FROM`.
var sqliteLimitPattern = regexp.MustCompile(`(?i)\blimit\b\s+\d+`)

func sqlHasExplicitLimit(sqlText string) bool {
	// Remove single-quoted strings so 'limit' inside literals doesn't match.
	stripped := stripSingleQuotedStrings(sqlText)
	return sqliteLimitPattern.MatchString(stripped)
}

// stripSingleQuotedStrings removes '...' sequences from SQL, replacing them
// with empty strings. Handles SQL-style '' escapes inside quoted strings.
func stripSingleQuotedStrings(s string) string {
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\'' {
			if inQuote && i+1 < len(s) && s[i+1] == '\'' {
				// Escaped quote — skip both
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}
		if !inQuote {
			b.WriteByte(ch)
		}
	}
	return b.String()
}
// sqliteExecuteRawQuery runs a raw SQL query, capping rows.
// Rejects bound parameters (?) — values must be inlined because the SQL comes
// from a single query string with no separate bind arguments. omp: paramsCount > 0.
func sqliteExecuteRawQuery(db *sql.DB, sqlText string) (string, error) {
	if strings.Contains(sqlText, "?") {
		return "", fmt.Errorf("sqlite raw queries do not support bound parameters (?) — inline values directly")
	}
	qRows, err := db.Query(sqlText)
	if err != nil {
		return "", fmt.Errorf("raw query: %w", err)
	}
	defer qRows.Close()

	colNames, err := qRows.Columns()
	if err != nil {
		return "", err
	}

	// Respect user-supplied LIMIT — they're explicitly opting into more rows.
	// Otherwise cap at sqliteMaxRawQueryRows as a context-window guard.
	capRows := !sqlHasExplicitLimit(sqlText)

	var rows []map[string]any
	truncated := false
	for qRows.Next() {
		if capRows && len(rows) >= sqliteMaxRawQueryRows {
			truncated = true
			break
		}
		vals := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := qRows.Scan(ptrs...); err != nil {
			return "", err
		}
		row := map[string]any{}
		for i, col := range colNames {
			row[col] = vals[i]
		}
		rows = append(rows, row)
	}

	result := buildSqliteOutput(colNames, rows)
	if truncated {
		result += fmt.Sprintf("\n[result truncated at %d rows]", sqliteMaxRawQueryRows)
	}
	return result, nil
}

// getTableColumns returns column names for a table.
// sqliteColumnInfo holds column metadata from PRAGMA table_info.
type sqliteColumnInfo struct {
	name string
	typ  string
	pk   int
}

// getTableInfo queries PRAGMA table_info once and returns column metadata.
// Replaces the previous getTableColumns + getTablePrimaryKey double-query pattern.
func getTableInfo(db *sql.DB, table string) ([]sqliteColumnInfo, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", quoteIdent(table)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []sqliteColumnInfo
	for rows.Next() {
		var ci sqliteColumnInfo
		var cid int
		var notnull int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &ci.name, &ci.typ, &notnull, &dflt, &ci.pk); err != nil {
			return nil, err
		}
		cols = append(cols, ci)
	}
	return cols, nil
}

func getTableColumns(db *sql.DB, table string) ([]string, error) {
	infos, err := getTableInfo(db, table)
	if err != nil {
		return nil, err
	}
	cols := make([]string, len(infos))
	for i, ci := range infos {
		cols[i] = ci.name
	}
	return cols, nil
}

// getTablePrimaryKey returns the single-column PK name+type, or empty strings if none/composite.
func getTablePrimaryKey(db *sql.DB, table string) (string, string, error) {
	infos, err := getTableInfo(db, table)
	if err != nil {
		return "", "", err
	}
	var pkCols []sqliteColumnInfo
	for _, ci := range infos {
		if ci.pk > 0 {
			pkCols = append(pkCols, ci)
		}
	}
	if len(pkCols) != 1 {
		return "", "", nil
	}
	return pkCols[0].name, pkCols[0].typ, nil
}

func coerceLookupValue(key, typ string) (any, error) {
	upper := strings.ToUpper(typ)
	if strings.Contains(upper, "INT") {
		n, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("primary key '%s' must be an integer; got '%s'", key, key)
		}
		return n, nil
	}
	if strings.Contains(upper, "REAL") || strings.Contains(upper, "FLOA") || strings.Contains(upper, "DOUB") {
		f, err := strconv.ParseFloat(key, 64)
		if err == nil {
			return f, nil
		}
	}
	return key, nil
}

func resolveOrderClause(order string, columns []string) (string, error) {
	trimmed := strings.TrimSpace(order)
	if trimmed == "" {
		return "", nil
	}

	col := trimmed
	direction := "ASC"
	if idx := strings.LastIndex(trimmed, ":"); idx != -1 {
		col = trimmed[:idx]
		direction = strings.ToUpper(strings.TrimSpace(trimmed[idx+1:]))
	}

	found := slices.Contains(columns, col)
	if !found {
		return "", fmt.Errorf("sqlite order column '%s' not found in table schema", col)
	}
	if direction != "ASC" && direction != "DESC" {
		return "", fmt.Errorf("sqlite order direction must be 'asc' or 'desc'; got '%s'", direction)
	}
	return fmt.Sprintf(" ORDER BY %s %s", quoteIdent(col), direction), nil
}

// stringifySqliteValue converts a DB value to display string.
func stringifySqliteValue(v any) string {
	if v == nil {
		return "NULL"
	}
	switch val := v.(type) {
	case string:
		return val
	case int, int32, int64, float32, float64, bool:
		return fmt.Sprintf("%v", val)
	case []byte:
		return fmt.Sprintf("<BLOB %d bytes>", len(val))
	default:
		return fmt.Sprintf("%v", val)
	}
}

// sanitizeCell escapes tabs and newlines for table rendering.
// Newlines become literal \n so a row stays on one line; tabs become spaces
// so they don't break TSV-style parsing.
func sanitizeCell(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r\n", "\\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// stringWidth returns the visible character count (rune count, ignoring ANSI).
func stringWidth(s string) int {
	return len([]rune(s))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildSqliteOutput renders query results in a LLM-friendly format that mirrors
// sqlite3 CLI defaults:
//   - Single column: raw values, one per line (no padding, no truncation).
//   - Multiple columns: header row + pipe-separated values per row.
//
// This format avoids the noise of padded ASCII tables (| --- |) and never
// truncates long values like CREATE statements, which LLMs need verbatim.
func buildSqliteOutput(columns []string, rows []map[string]any) string {
	if len(columns) == 0 {
		if len(rows) == 0 {
			return "(no rows)"
		}
		return "(rows returned without named columns)"
	}

	// Single column: raw values, one per line.
	if len(columns) == 1 {
		col := columns[0]
		if len(rows) == 0 {
			return "(no rows)"
		}
		lines := make([]string, 0, len(rows))
		for _, row := range rows {
			lines = append(lines, sanitizeCell(stringifySqliteValue(row[col])))
		}
		return strings.Join(lines, "\n")
	}

	// Multi-column: pipe-separated with header.
	header := strings.Join(columns, "|")
	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, header)
	if len(rows) == 0 {
		return header + "\n(no rows)"
	}
	for _, row := range rows {
		vals := make([]string, len(columns))
		for i, col := range columns {
			vals[i] = sanitizeCell(stringifySqliteValue(row[col]))
		}
		lines = append(lines, strings.Join(vals, "|"))
	}
	return strings.Join(lines, "\n")
}

// trySqlitePath attempts to handle the input as a SQLite path.
// Returns (result, true, nil) if handled, (nil, false, nil) if not a SQLite path,
// (nil, true, err) if it is a SQLite path but an error occurred.
func trySqlitePath(ctx context.Context, in Input) (*tool.ToolResult, bool, error) {
	candidates := parseSqlitePathCandidates(in.FilePath)
	if len(candidates) == 0 {
		return nil, false, nil
	}
	// Check if any candidate resolves to a real SQLite file
	for _, c := range candidates {
		ap, err := filepath.Abs(c.sqlitePath)
		if err != nil {
			continue
		}
		info, err := os.Stat(ap)
		if err != nil || info.IsDir() {
			continue
		}
		if !isSqliteFile(ap) {
			continue
		}
		// It's a real SQLite file — handle it. Pass c + ap to avoid re-scanning.
		result, err := executeSqliteRead(ctx, in, c, ap)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil
	}
	return nil, false, nil
}
