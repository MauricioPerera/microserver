package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

const collectionsSchema = `CREATE TABLE IF NOT EXISTS collections (
	name TEXT PRIMARY KEY,
	table_name TEXT NOT NULL,
	has_vector INTEGER NOT NULL,
	dimensions INTEGER,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

// collectionNameRe whitelists what a collection name may look like. This is
// the entire injection defense for building CREATE/DROP TABLE statements:
// SQLite has no way to bind an identifier as a query parameter, so any
// table name that ends up in DDL must come from a value already checked
// against this pattern — never straight from request input.
var collectionNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,62}$`)

const maxDimensions = 8192

var (
	ErrCollectionExists      = errors.New("collection already exists")
	ErrCollectionNotFound    = errors.New("collection not found")
	ErrCollectionNotVector   = errors.New("collection has no vectors")
	ErrInvalidCollectionName = fmt.Errorf("collection name must match %s", collectionNameRe.String())
)

// quoteIdent double-quotes a SQL identifier, escaping embedded quotes.
// Defense in depth on top of collectionNameRe — table names built from a
// regex-validated name shouldn't contain a '"' to begin with.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Collection is a row from the collections registry.
type Collection struct {
	Name       string `json:"name"`
	HasVector  bool   `json:"has_vector"`
	Dimensions int    `json:"dimensions,omitempty"`
	tableName  string
}

// createCollection registers a new collection and creates its backing
// table. Vector collections get the same float32+binary dual storage as
// the fixed vec_items table, plus a free-form "data" JSON column for
// metadata unrelated to the embedding. Non-vector collections are a plain
// table: just id + data.
func createCollection(s *VecStore, name string, hasVector bool, dimensions int) error {
	if !collectionNameRe.MatchString(name) {
		return ErrInvalidCollectionName
	}
	if hasVector && (dimensions <= 0 || dimensions > maxDimensions) {
		return fmt.Errorf("dimensions must be between 1 and %d when vector is true", maxDimensions)
	}

	tx, err := s.write.Begin()
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM collections WHERE name = ?`, name).Scan(&exists); err != nil {
		return fmt.Errorf("checking existing collection: %w", err)
	}
	if exists > 0 {
		return ErrCollectionExists
	}

	tableName := "coll_" + name
	quoted := quoteIdent(tableName)

	var ddl string
	if hasVector {
		ddl = fmt.Sprintf(`CREATE VIRTUAL TABLE %s USING vec0(
			id INTEGER PRIMARY KEY,
			embedding FLOAT[%d],
			embedding_bit BIT[%d],
			+text TEXT,
			+data TEXT
		)`, quoted, dimensions, dimensions)
	} else {
		ddl = fmt.Sprintf(`CREATE TABLE %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			data TEXT NOT NULL
		)`, quoted)
	}
	if _, err := tx.Exec(ddl); err != nil {
		return fmt.Errorf("creating collection table: %w", err)
	}

	var dimsArg any
	if hasVector {
		dimsArg = dimensions
	}
	if _, err := tx.Exec(
		`INSERT INTO collections(name, table_name, has_vector, dimensions) VALUES (?, ?, ?, ?)`,
		name, tableName, hasVector, dimsArg,
	); err != nil {
		return fmt.Errorf("registering collection: %w", err)
	}

	return tx.Commit()
}

// getCollection looks up a collection by name. Returns ErrCollectionNotFound
// if it doesn't exist.
func getCollection(s *VecStore, name string) (*Collection, error) {
	var c Collection
	var hasVector int
	var dims sql.NullInt64
	err := s.read.QueryRow(
		`SELECT name, table_name, has_vector, dimensions FROM collections WHERE name = ?`, name,
	).Scan(&c.Name, &c.tableName, &hasVector, &dims)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCollectionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("looking up collection: %w", err)
	}
	c.HasVector = hasVector != 0
	if dims.Valid {
		c.Dimensions = int(dims.Int64)
	}
	return &c, nil
}

// listCollections returns all registered collections.
func listCollections(s *VecStore) ([]Collection, error) {
	rows, err := s.read.Query(`SELECT name, table_name, has_vector, dimensions FROM collections ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing collections: %w", err)
	}
	defer rows.Close()

	cols := []Collection{}
	for rows.Next() {
		var c Collection
		var hasVector int
		var dims sql.NullInt64
		if err := rows.Scan(&c.Name, &c.tableName, &hasVector, &dims); err != nil {
			return nil, fmt.Errorf("scanning collection: %w", err)
		}
		c.HasVector = hasVector != 0
		if dims.Valid {
			c.Dimensions = int(dims.Int64)
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// dropCollection removes a collection and its backing table. Returns
// ErrCollectionNotFound if it doesn't exist.
func dropCollection(s *VecStore, name string) error {
	coll, err := getCollection(s, name)
	if err != nil {
		return err
	}

	tx, err := s.write.Begin()
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(fmt.Sprintf(`DROP TABLE %s`, quoteIdent(coll.tableName))); err != nil {
		return fmt.Errorf("dropping collection table: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM collections WHERE name = ?`, name); err != nil {
		return fmt.Errorf("deregistering collection: %w", err)
	}
	return tx.Commit()
}

// Document is an item in a collection. Text is only meaningful for vector
// collections (it's what got embedded); Data is arbitrary JSON metadata,
// present in both vector and non-vector collections.
type Document struct {
	ID       int64           `json:"id"`
	Text     string          `json:"text,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Distance *float64        `json:"distance,omitempty"`
}

func dataOrEmpty(data json.RawMessage) string {
	if len(data) == 0 {
		return "{}"
	}
	return string(data)
}

// insertDocument adds an item to a collection. text is required and gets
// embedded if the collection has vectors; ignored otherwise. id nil means
// let SQLite assign one.
func insertDocument(s *VecStore, coll *Collection, id *int64, text string, data json.RawMessage) (int64, error) {
	table := quoteIdent(coll.tableName)
	dataStr := dataOrEmpty(data)

	if coll.HasVector {
		vec, err := embed(text)
		if err != nil {
			return 0, fmt.Errorf("embedding text: %w", err)
		}
		serialized, err := sqlite_vec.SerializeFloat32(vec)
		if err != nil {
			return 0, fmt.Errorf("serializing vector: %w", err)
		}
		if id != nil {
			if _, err := s.write.Exec(
				fmt.Sprintf(`INSERT INTO %s(id, embedding, embedding_bit, text, data) VALUES (?, ?, vec_quantize_binary(?), ?, ?)`, table),
				*id, serialized, serialized, text, dataStr,
			); err != nil {
				return 0, fmt.Errorf("inserting document: %w", err)
			}
			return *id, nil
		}
		res, err := s.write.Exec(
			fmt.Sprintf(`INSERT INTO %s(embedding, embedding_bit, text, data) VALUES (?, vec_quantize_binary(?), ?, ?)`, table),
			serialized, serialized, text, dataStr,
		)
		if err != nil {
			return 0, fmt.Errorf("inserting document: %w", err)
		}
		return res.LastInsertId()
	}

	if id != nil {
		if _, err := s.write.Exec(fmt.Sprintf(`INSERT INTO %s(id, data) VALUES (?, ?)`, table), *id, dataStr); err != nil {
			return 0, fmt.Errorf("inserting document: %w", err)
		}
		return *id, nil
	}
	res, err := s.write.Exec(fmt.Sprintf(`INSERT INTO %s(data) VALUES (?)`, table), dataStr)
	if err != nil {
		return 0, fmt.Errorf("inserting document: %w", err)
	}
	return res.LastInsertId()
}

// updateDocument replaces an existing item's text/data. For vector
// collections this is delete+insert in a transaction, same workaround as
// updateText on the fixed table — vec0's UPDATE path doesn't handle typed
// vector blobs correctly. Returns false if no row had that id.
func updateDocument(s *VecStore, coll *Collection, id int64, text string, data json.RawMessage) (bool, error) {
	table := quoteIdent(coll.tableName)
	dataStr := dataOrEmpty(data)

	if !coll.HasVector {
		res, err := s.write.Exec(fmt.Sprintf(`UPDATE %s SET data = ? WHERE id = ?`, table), dataStr, id)
		if err != nil {
			return false, fmt.Errorf("updating document: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("checking rows affected: %w", err)
		}
		return n > 0, nil
	}

	vec, err := embed(text)
	if err != nil {
		return false, fmt.Errorf("embedding text: %w", err)
	}
	serialized, err := sqlite_vec.SerializeFloat32(vec)
	if err != nil {
		return false, fmt.Errorf("serializing vector: %w", err)
	}

	tx, err := s.write.Begin()
	if err != nil {
		return false, fmt.Errorf("starting update transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, table), id)
	if err != nil {
		return false, fmt.Errorf("updating document (delete step): %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return false, nil
	}

	if _, err := tx.Exec(
		fmt.Sprintf(`INSERT INTO %s(id, embedding, embedding_bit, text, data) VALUES (?, ?, vec_quantize_binary(?), ?, ?)`, table),
		id, serialized, serialized, text, dataStr,
	); err != nil {
		return false, fmt.Errorf("updating document (insert step): %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("committing update: %w", err)
	}
	return true, nil
}

// deleteDocument removes an item by id. Returns false if no row had that id.
func deleteDocument(s *VecStore, coll *Collection, id int64) (bool, error) {
	table := quoteIdent(coll.tableName)
	res, err := s.write.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, table), id)
	if err != nil {
		return false, fmt.Errorf("deleting document: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking rows affected: %w", err)
	}
	return n > 0, nil
}

// listDocuments returns items in a collection, ascending id order, paginated.
func listDocuments(s *VecStore, coll *Collection, limit, offset int) ([]Document, error) {
	table := quoteIdent(coll.tableName)

	var query string
	if coll.HasVector {
		query = fmt.Sprintf(`SELECT id, text, data FROM %s ORDER BY id LIMIT ? OFFSET ?`, table)
	} else {
		query = fmt.Sprintf(`SELECT id, data FROM %s ORDER BY id LIMIT ? OFFSET ?`, table)
	}
	rows, err := s.read.Query(query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing documents: %w", err)
	}
	defer rows.Close()

	docs := []Document{}
	for rows.Next() {
		var d Document
		var data string
		if coll.HasVector {
			if err := rows.Scan(&d.ID, &d.Text, &data); err != nil {
				return nil, fmt.Errorf("scanning document: %w", err)
			}
		} else {
			if err := rows.Scan(&d.ID, &data); err != nil {
				return nil, fmt.Errorf("scanning document: %w", err)
			}
		}
		d.Data = json.RawMessage(data)
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// searchDocuments runs KNN search within a vector collection. Same
// rerank/binary-only tradeoff as queryText on the fixed table. Returns
// ErrCollectionNotVector for collections without vectors.
func searchDocuments(s *VecStore, coll *Collection, text string, limit int, rerank bool) ([]Document, error) {
	if !coll.HasVector {
		return nil, ErrCollectionNotVector
	}
	table := quoteIdent(coll.tableName)

	vec, err := embed(text)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}
	serialized, err := sqlite_vec.SerializeFloat32(vec)
	if err != nil {
		return nil, fmt.Errorf("serializing vector: %w", err)
	}

	var rows *sql.Rows
	if !rerank {
		rows, err = s.read.Query(fmt.Sprintf(`
			SELECT id, text, data, distance
			FROM %s
			WHERE embedding_bit MATCH vec_quantize_binary(?)
			ORDER BY distance
			LIMIT ?`, table), serialized, limit)
	} else {
		rows, err = s.read.Query(fmt.Sprintf(`
			WITH coarse_matches AS (
				SELECT id, embedding, text, data
				FROM %s
				WHERE embedding_bit MATCH vec_quantize_binary(?)
				ORDER BY distance
				LIMIT ?
			)
			SELECT id, text, data, vec_distance_L2(embedding, ?) AS distance
			FROM coarse_matches
			ORDER BY distance
			LIMIT ?`, table), serialized, limit*oversampleFactor, serialized, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("searching documents: %w", err)
	}
	defer rows.Close()

	docs := []Document{}
	for rows.Next() {
		var d Document
		var data string
		var distance float64
		if err := rows.Scan(&d.ID, &d.Text, &data, &distance); err != nil {
			return nil, fmt.Errorf("scanning document: %w", err)
		}
		d.Data = json.RawMessage(data)
		d.Distance = &distance
		docs = append(docs, d)
	}
	return docs, rows.Err()
}
