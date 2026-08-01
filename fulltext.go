package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrCollectionNoFullText is returned when full-text search is attempted on
// a collection with no text field — only vector collections have one
// (what got embedded); non-vector collections only ever have "data".
var ErrCollectionNoFullText = errors.New("collection has no text field to search")

// execer is the subset of *sql.DB / *sql.Tx used by the full-text index
// helpers, so they compose into an existing transaction (updateDocument,
// deleteDocument) or run standalone (insertDocument, createCollection).
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func ftsTableName(coll *Collection) string {
	return coll.tableName + "_fts"
}

// createFullTextIndex creates the FTS5 companion table for a vector
// collection. Deliberately a plain standalone FTS5 table, manually synced
// from Go on every insert/update/delete below — not FTS5's "external
// content" mode with triggers on the main table, since the main table is a
// vec0 virtual table and trigger support on it hasn't been verified. This
// needs the whole binary built with -tags sqlite_fts5; without it, SQLite
// has no fts5 module compiled in at all (confirmed: "no such module: fts5").
func createFullTextIndex(ex execer, coll *Collection) error {
	if _, err := ex.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE %s USING fts5(text)`, quoteIdent(ftsTableName(coll)))); err != nil {
		return fmt.Errorf("creating full-text index: %w", err)
	}
	return nil
}

func dropFullTextIndex(ex execer, coll *Collection) error {
	if _, err := ex.Exec(fmt.Sprintf(`DROP TABLE %s`, quoteIdent(ftsTableName(coll)))); err != nil {
		return fmt.Errorf("dropping full-text index: %w", err)
	}
	return nil
}

// insertFullTextRow indexes text under the same id as the main table's row
// (FTS5's rowid, set explicitly rather than left to auto-assign) so the two
// tables stay 1:1 joinable by id.
func insertFullTextRow(ex execer, coll *Collection, id int64, text string) error {
	if _, err := ex.Exec(fmt.Sprintf(`INSERT INTO %s(rowid, text) VALUES (?, ?)`, quoteIdent(ftsTableName(coll))), id, text); err != nil {
		return fmt.Errorf("indexing text for full-text search: %w", err)
	}
	return nil
}

func deleteFullTextRow(ex execer, coll *Collection, id int64) error {
	if _, err := ex.Exec(fmt.Sprintf(`DELETE FROM %s WHERE rowid = ?`, quoteIdent(ftsTableName(coll))), id); err != nil {
		return fmt.Errorf("removing text from full-text index: %w", err)
	}
	return nil
}

// fullTextSearchDocuments runs an FTS5 query over a vector collection's
// text field, ranked by relevance (bm25, via FTS5's built-in "rank" column)
// unless sort overrides it — same "selection stays as-is, sort only
// reorders" semantics as searchDocuments. q is passed through as FTS5 query
// syntax as-is: AND/OR/NOT, "exact phrase", prefix* etc. are all valid and
// intentional, not sanitized away.
//
// Returns ErrCollectionNoFullText for non-vector collections. Any other
// error here (almost always malformed FTS5 query syntax from the caller,
// e.g. unbalanced quotes) is the HTTP layer's to turn into a 400, not a 500.
func fullTextSearchDocuments(s *VecStore, coll *Collection, query string, limit, offset int, filters []filterCondition, sort *sortSpec) ([]Document, error) {
	if !coll.HasVector {
		return nil, ErrCollectionNoFullText
	}
	table := quoteIdent(coll.tableName)
	ftsTable := quoteIdent(ftsTableName(coll))

	whereSQL, whereArgs := filterWhereSQL(filters)
	extraWhere := ""
	if whereSQL != "" {
		extraWhere = "AND " + whereSQL
	}

	orderBy := "rank"
	var orderArgs []any
	if sort != nil {
		orderBy, orderArgs = sort.orderBySQL()
	}

	sqlText := fmt.Sprintf(`
		SELECT m.id, m.text, m.data
		FROM %s f
		JOIN %s m ON m.id = f.rowid
		WHERE f.text MATCH ? %s
		ORDER BY %s
		LIMIT ? OFFSET ?`, ftsTable, table, extraWhere, orderBy)

	args := append([]any{query}, whereArgs...)
	args = append(args, orderArgs...)
	args = append(args, limit, offset)

	rows, err := s.read.Query(sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("full-text search: %w", err)
	}
	defer rows.Close()

	docs := []Document{}
	for rows.Next() {
		var d Document
		var data string
		if err := rows.Scan(&d.ID, &d.Text, &data); err != nil {
			return nil, fmt.Errorf("scanning document: %w", err)
		}
		d.Data = json.RawMessage(data)
		docs = append(docs, d)
	}
	return docs, rows.Err()
}
