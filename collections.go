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

// A reference is declared when its owning collection is created: a named
// top-level field in that collection's "data" JSON that must hold either
// null or the id of an existing row in target_collection. field_name and
// target_collection are never interpolated into SQL — they're always bound
// as query parameters (json_extract's path argument is an ordinary bindable
// string, so this needs no identifier whitelist the way table names do).
const referencesSchema = `CREATE TABLE IF NOT EXISTS collection_references (
	collection_name TEXT NOT NULL,
	field_name TEXT NOT NULL,
	target_collection TEXT NOT NULL,
	on_delete TEXT NOT NULL,
	PRIMARY KEY (collection_name, field_name)
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
	ErrDocumentNotFound      = errors.New("document not found")
)

// ReferenceSpec is a request to declare a reference field when creating a
// collection.
type ReferenceSpec struct {
	Collection string `json:"collection"`
	OnDelete   string `json:"on_delete,omitempty"` // "restrict" (default) or "set_null"
}

// Reference is a stored reference declaration, as returned to clients.
type Reference struct {
	Field      string `json:"field"`
	Collection string `json:"collection"`
	OnDelete   string `json:"on_delete"`
}

const (
	onDeleteRestrict = "restrict"
	onDeleteSetNull  = "set_null"
)

// ReferenceValidationError means a request's reference field was malformed
// or pointed at something that doesn't exist — a client (400) problem.
type ReferenceValidationError struct{ msg string }

func (e *ReferenceValidationError) Error() string { return e.msg }

// ReferentialConstraintError means a delete was refused because another
// collection still has a restrict-mode reference pointing at the row — a
// conflict (409), not a bad request.
type ReferentialConstraintError struct{ msg string }

func (e *ReferentialConstraintError) Error() string { return e.msg }

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
//
// references declares fields in this collection's data that must point at
// an existing row in another (already-created) collection — the closest
// thing to a foreign key this store has. Validated on every insert/update;
// on_delete controls what happens to referencing rows when the referenced
// row is deleted (see checkReferentialDelete).
func createCollection(s *VecStore, name string, hasVector bool, dimensions int, references map[string]ReferenceSpec) error {
	if !collectionNameRe.MatchString(name) {
		return ErrInvalidCollectionName
	}
	if hasVector && (dimensions <= 0 || dimensions > maxDimensions) {
		return fmt.Errorf("dimensions must be between 1 and %d when vector is true", maxDimensions)
	}
	for field, ref := range references {
		if !collectionNameRe.MatchString(field) {
			return fmt.Errorf("reference field name %q must match %s", field, collectionNameRe.String())
		}
		if ref.OnDelete != "" && ref.OnDelete != onDeleteRestrict && ref.OnDelete != onDeleteSetNull {
			return fmt.Errorf("reference field %q: on_delete must be %q or %q", field, onDeleteRestrict, onDeleteSetNull)
		}
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

	for _, ref := range references {
		var targetExists int
		if err := tx.QueryRow(`SELECT count(*) FROM collections WHERE name = ?`, ref.Collection).Scan(&targetExists); err != nil {
			return fmt.Errorf("checking reference target %q: %w", ref.Collection, err)
		}
		if targetExists == 0 {
			return fmt.Errorf("reference target collection %q does not exist (create it first)", ref.Collection)
		}
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
	if hasVector {
		if err := createFullTextIndex(tx, &Collection{tableName: tableName}); err != nil {
			return err
		}
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

	for field, ref := range references {
		onDelete := ref.OnDelete
		if onDelete == "" {
			onDelete = onDeleteRestrict
		}
		if _, err := tx.Exec(
			`INSERT INTO collection_references(collection_name, field_name, target_collection, on_delete) VALUES (?, ?, ?, ?)`,
			name, field, ref.Collection, onDelete,
		); err != nil {
			return fmt.Errorf("registering reference %q: %w", field, err)
		}
	}

	return tx.Commit()
}

// getReferences returns the reference fields declared on a collection.
func getReferences(s *VecStore, collectionName string) ([]Reference, error) {
	rows, err := s.read.Query(
		`SELECT field_name, target_collection, on_delete FROM collection_references WHERE collection_name = ?`,
		collectionName,
	)
	if err != nil {
		return nil, fmt.Errorf("looking up references: %w", err)
	}
	defer rows.Close()

	var refs []Reference
	for rows.Next() {
		var r Reference
		if err := rows.Scan(&r.Field, &r.Collection, &r.OnDelete); err != nil {
			return nil, fmt.Errorf("scanning reference: %w", err)
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// referenceDependent is a reference declared by some other collection that
// points at collectionName — i.e. rows there that might point at a row
// about to be deleted here.
type referenceDependent struct {
	CollectionName string
	Field          string
	OnDelete       string
}

func getReferencesTargeting(s *VecStore, collectionName string) ([]referenceDependent, error) {
	rows, err := s.read.Query(
		`SELECT collection_name, field_name, on_delete FROM collection_references WHERE target_collection = ?`,
		collectionName,
	)
	if err != nil {
		return nil, fmt.Errorf("looking up dependent references: %w", err)
	}
	defer rows.Close()

	var deps []referenceDependent
	for rows.Next() {
		var d referenceDependent
		if err := rows.Scan(&d.CollectionName, &d.Field, &d.OnDelete); err != nil {
			return nil, fmt.Errorf("scanning dependent reference: %w", err)
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

// validateReferences checks every reference field present (and non-null) in
// data against its target collection, failing if the referenced id doesn't
// exist there. Fields that are absent or null are treated as unset — no
// "required reference" support yet.
func validateReferences(s *VecStore, collectionName string, data json.RawMessage) error {
	refs, err := getReferences(s, collectionName)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}

	var parsed map[string]json.RawMessage
	if len(data) > 0 {
		if err := json.Unmarshal(data, &parsed); err != nil {
			return &ReferenceValidationError{fmt.Sprintf("data must be a JSON object to validate its reference fields: %v", err)}
		}
	}

	for _, ref := range refs {
		raw, present := parsed[ref.Field]
		if !present || string(raw) == "null" {
			continue
		}
		var refID int64
		if err := json.Unmarshal(raw, &refID); err != nil {
			return &ReferenceValidationError{fmt.Sprintf("field %q must be an integer id or null", ref.Field)}
		}
		target, err := getCollection(s, ref.Collection)
		if err != nil {
			return &ReferenceValidationError{fmt.Sprintf("reference target collection %q: %v", ref.Collection, err)}
		}
		var count int
		table := quoteIdent(target.tableName)
		if err := s.read.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s WHERE id = ?`, table), refID).Scan(&count); err != nil {
			return fmt.Errorf("checking reference %q: %w", ref.Field, err)
		}
		if count == 0 {
			return &ReferenceValidationError{fmt.Sprintf("field %q references id %d, which does not exist in collection %q", ref.Field, refID, ref.Collection)}
		}
	}
	return nil
}

// checkReferentialDelete enforces on_delete behavior for every other
// collection's reference field that points at collectionName, for the row
// about to be deleted there. Called before the row is actually removed:
//   - restrict: refuses the delete if any referencing row exists.
//   - set_null: nulls the field out on every referencing row.
func checkReferentialDelete(s *VecStore, collectionName string, id int64) error {
	deps, err := getReferencesTargeting(s, collectionName)
	if err != nil {
		return err
	}

	for _, dep := range deps {
		srcColl, err := getCollection(s, dep.CollectionName)
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				continue
			}
			return err
		}
		table := quoteIdent(srcColl.tableName)
		rows, err := s.read.Query(fmt.Sprintf(`SELECT id FROM %s WHERE json_extract(data, ?) = ?`, table), "$."+dep.Field, id)
		if err != nil {
			return fmt.Errorf("checking references from %q: %w", dep.CollectionName, err)
		}
		var matching []int64
		for rows.Next() {
			var rid int64
			if err := rows.Scan(&rid); err != nil {
				rows.Close()
				return fmt.Errorf("scanning referencing row: %w", err)
			}
			matching = append(matching, rid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(matching) == 0 {
			continue
		}

		switch dep.OnDelete {
		case onDeleteRestrict:
			return &ReferentialConstraintError{fmt.Sprintf("cannot delete: referenced by %d item(s) in collection %q (field %q)", len(matching), dep.CollectionName, dep.Field)}
		case onDeleteSetNull:
			for _, rid := range matching {
				doc, err := getDocumentByID(s, srcColl, rid)
				if err != nil {
					return fmt.Errorf("loading referencing document %d: %w", rid, err)
				}
				var m map[string]json.RawMessage
				if err := json.Unmarshal(dataOrEmptyBytes(doc.Data), &m); err != nil {
					return fmt.Errorf("parsing referencing document %d: %w", rid, err)
				}
				m[dep.Field] = json.RawMessage("null")
				newData, err := json.Marshal(m)
				if err != nil {
					return fmt.Errorf("re-encoding referencing document %d: %w", rid, err)
				}
				if _, err := updateDocument(s, srcColl, rid, doc.Text, newData); err != nil {
					return fmt.Errorf("nulling reference on document %d: %w", rid, err)
				}
			}
		}
	}
	return nil
}

func dataOrEmptyBytes(data json.RawMessage) []byte {
	if len(data) == 0 {
		return []byte("{}")
	}
	return data
}

// getDocumentByID fetches a single item by id. Returns ErrDocumentNotFound
// if it doesn't exist.
func getDocumentByID(s *VecStore, coll *Collection, id int64) (*Document, error) {
	table := quoteIdent(coll.tableName)
	var d Document
	var data string
	var err error
	if coll.HasVector {
		err = s.read.QueryRow(fmt.Sprintf(`SELECT id, text, data FROM %s WHERE id = ?`, table), id).Scan(&d.ID, &d.Text, &data)
	} else {
		err = s.read.QueryRow(fmt.Sprintf(`SELECT id, data FROM %s WHERE id = ?`, table), id).Scan(&d.ID, &data)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDocumentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting document: %w", err)
	}
	d.Data = json.RawMessage(data)
	return &d, nil
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
	if coll.HasVector {
		if err := dropFullTextIndex(tx, coll); err != nil {
			return err
		}
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
// let SQLite assign one. Any reference fields declared on this collection
// are validated against their target collections before the write.
func insertDocument(s *VecStore, coll *Collection, id *int64, text string, data json.RawMessage) (int64, error) {
	if err := validateReferences(s, coll.Name, data); err != nil {
		return 0, err
	}
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
		var finalID int64
		if id != nil {
			if _, err := s.write.Exec(
				fmt.Sprintf(`INSERT INTO %s(id, embedding, embedding_bit, text, data) VALUES (?, ?, vec_quantize_binary(?), ?, ?)`, table),
				*id, serialized, serialized, text, dataStr,
			); err != nil {
				return 0, fmt.Errorf("inserting document: %w", err)
			}
			finalID = *id
		} else {
			res, err := s.write.Exec(
				fmt.Sprintf(`INSERT INTO %s(embedding, embedding_bit, text, data) VALUES (?, vec_quantize_binary(?), ?, ?)`, table),
				serialized, serialized, text, dataStr,
			)
			if err != nil {
				return 0, fmt.Errorf("inserting document: %w", err)
			}
			finalID, err = res.LastInsertId()
			if err != nil {
				return 0, fmt.Errorf("getting inserted id: %w", err)
			}
		}
		if err := insertFullTextRow(s.write, coll, finalID, text); err != nil {
			return 0, err
		}
		return finalID, nil
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
// vector blobs correctly. Returns false if no row had that id. Reference
// fields are validated the same way as on insert.
func updateDocument(s *VecStore, coll *Collection, id int64, text string, data json.RawMessage) (bool, error) {
	if err := validateReferences(s, coll.Name, data); err != nil {
		return false, err
	}
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

	if err := deleteFullTextRow(tx, coll, id); err != nil {
		return false, err
	}
	if err := insertFullTextRow(tx, coll, id, text); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("committing update: %w", err)
	}
	return true, nil
}

// deleteDocument removes an item by id. Returns false if no row had that
// id. Before deleting, enforces on_delete for any other collection's
// reference field pointing at this row (see checkReferentialDelete). For
// vector collections, the full-text index row is removed in the same
// transaction so the two never drift out of sync.
func deleteDocument(s *VecStore, coll *Collection, id int64) (bool, error) {
	if err := checkReferentialDelete(s, coll.Name, id); err != nil {
		return false, err
	}
	table := quoteIdent(coll.tableName)

	tx, err := s.write.Begin()
	if err != nil {
		return false, fmt.Errorf("starting delete transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, table), id)
	if err != nil {
		return false, fmt.Errorf("deleting document: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return false, nil
	}

	if coll.HasVector {
		if err := deleteFullTextRow(tx, coll, id); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("committing delete: %w", err)
	}
	return true, nil
}

// listDocuments returns items in a collection, ascending id order, paginated,
// optionally restricted by filters on top-level fields of data.
func listDocuments(s *VecStore, coll *Collection, limit, offset int, filters []filterCondition, sort *sortSpec) ([]Document, error) {
	table := quoteIdent(coll.tableName)
	whereSQL, whereArgs := filterWhereSQL(filters)
	where := ""
	if whereSQL != "" {
		where = "WHERE " + whereSQL
	}

	orderBy := "id"
	var orderArgs []any
	if sort != nil {
		orderBy, orderArgs = sort.orderBySQL()
	}

	var query string
	if coll.HasVector {
		query = fmt.Sprintf(`SELECT id, text, data FROM %s %s ORDER BY %s LIMIT ? OFFSET ?`, table, where, orderBy)
	} else {
		query = fmt.Sprintf(`SELECT id, data FROM %s %s ORDER BY %s LIMIT ? OFFSET ?`, table, where, orderBy)
	}
	args := append(append([]any{}, whereArgs...), orderArgs...)
	args = append(args, limit, offset)
	rows, err := s.read.Query(query, args...)
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

// searchDocuments runs KNN search within a vector collection, optionally
// restricted by filters on top-level fields of data. Same rerank/binary-only
// tradeoff as queryText on the fixed table. Returns ErrCollectionNotVector
// for collections without vectors.
//
// sort, if non-nil, reorders the final results by a data field instead of
// distance — the candidate *selection* is still governed by vector
// similarity (plus filters); sort only changes presentation order among
// whatever got selected.
//
// Filtered search is best-effort, not exact: sqlite-vec's vec0 requires a
// "k = N" bound alongside any extra WHERE condition, and that k limits how
// many nearest-by-vector candidates are scanned *before* the filter is
// applied — confirmed empirically, it does not filter first and then find
// the K nearest among matches. A filter value that's rare and semantically
// far from the query (e.g. one document out of thousands, unrelated to the
// query text) can be missed entirely even though it's the only match. k is
// set to limit*oversampleFactor as a mitigation, same knob used for rerank,
// not a guarantee.
func searchDocuments(s *VecStore, coll *Collection, text string, limit int, rerank bool, filters []filterCondition, sort *sortSpec) ([]Document, error) {
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

	whereSQL, whereArgs := filterWhereSQL(filters)
	extraWhere := ""
	if whereSQL != "" {
		extraWhere = "AND " + whereSQL
	}

	finalOrderBy := "distance"
	var sortArgs []any
	if sort != nil {
		finalOrderBy, sortArgs = sort.orderBySQL()
	}

	var rows *sql.Rows
	if !rerank {
		// vec0 only accepts the bare "MATCH ... ORDER BY distance LIMIT ?"
		// form without an explicit "k = N" bound (verified empirically) —
		// any extra WHERE condition, or any ORDER BY other than distance,
		// needs k spelled out or the query is rejected outright.
		if len(filters) == 0 && sort == nil {
			rows, err = s.read.Query(fmt.Sprintf(`
				SELECT id, text, data, distance
				FROM %s
				WHERE embedding_bit MATCH vec_quantize_binary(?)
				ORDER BY distance
				LIMIT ?`, table), serialized, limit)
		} else {
			k := limit * oversampleFactor
			args := append([]any{serialized, k}, whereArgs...)
			args = append(args, sortArgs...)
			args = append(args, limit)
			rows, err = s.read.Query(fmt.Sprintf(`
				SELECT id, text, data, distance
				FROM %s
				WHERE embedding_bit MATCH vec_quantize_binary(?) AND k = ? %s
				ORDER BY %s
				LIMIT ?`, table, extraWhere, finalOrderBy), args...)
		}
	} else {
		coarseLimit := limit * oversampleFactor
		// The coarse CTE always orders by distance internally regardless of
		// sort — sort only changes the final presentation order over the
		// candidates coarse selection already picked, never which
		// candidates get picked.
		if len(filters) == 0 {
			args := []any{serialized, coarseLimit, serialized}
			args = append(args, sortArgs...)
			args = append(args, limit)
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
				ORDER BY %s
				LIMIT ?`, table, finalOrderBy), args...)
		} else {
			args := append([]any{serialized, coarseLimit}, whereArgs...)
			args = append(args, coarseLimit, serialized)
			args = append(args, sortArgs...)
			args = append(args, limit)
			rows, err = s.read.Query(fmt.Sprintf(`
				WITH coarse_matches AS (
					SELECT id, embedding, text, data
					FROM %s
					WHERE embedding_bit MATCH vec_quantize_binary(?) AND k = ? %s
					ORDER BY distance
					LIMIT ?
				)
				SELECT id, text, data, vec_distance_L2(embedding, ?) AS distance
				FROM coarse_matches
				ORDER BY %s
				LIMIT ?`, table, extraWhere, finalOrderBy), args...)
		}
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
