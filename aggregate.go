package main

import (
	"database/sql"
	"fmt"
)

// aggOps maps the client-facing op name to the literal SQL aggregate
// function. Like filterOps/sort direction, the SQL function name always
// comes from this map — never straight from request input.
var aggOps = map[string]string{
	"count": "COUNT",
	"sum":   "SUM",
	"avg":   "AVG",
	"min":   "MIN",
	"max":   "MAX",
}

// AggregateResult is one row of an aggregation. Group is nil when there's
// no group_by (a single overall result). Value is nil when the underlying
// SQL aggregate returned NULL — e.g. SUM/AVG/MIN/MAX over zero matching
// rows — rather than being coerced to 0, which would misrepresent "no
// data" as "data summing to zero".
type AggregateResult struct {
	Group *string  `json:"group,omitempty"`
	Value *float64 `json:"value"`
}

// aggregateDocuments computes an aggregate over a top-level data field,
// optionally grouped by another top-level field and restricted by filters.
// field is required for sum/avg/min/max; optional for count (count(field)
// counts non-null occurrences; omitted means count(*), all matching rows).
// Works the same for vector and non-vector collections — it never touches
// the embedding columns.
func aggregateDocuments(s *VecStore, coll *Collection, op, field, groupBy string, filters []filterCondition) ([]AggregateResult, error) {
	sqlFn, ok := aggOps[op]
	if !ok {
		return nil, fmt.Errorf("unknown aggregate operator %q (allowed: count, sum, avg, min, max)", op)
	}
	if op != "count" && field == "" {
		return nil, fmt.Errorf("field is required for %q", op)
	}
	if field != "" && !collectionNameRe.MatchString(field) {
		return nil, fmt.Errorf("invalid field %q", field)
	}
	if groupBy != "" && !collectionNameRe.MatchString(groupBy) {
		return nil, fmt.Errorf("invalid group_by field %q", groupBy)
	}

	table := quoteIdent(coll.tableName)
	whereSQL, whereArgs := filterWhereSQL(filters)
	where := ""
	if whereSQL != "" {
		where = "WHERE " + whereSQL
	}

	var aggExpr string
	var aggArgs []any
	if field != "" {
		aggExpr = fmt.Sprintf("%s(json_extract(data, ?))", sqlFn)
		aggArgs = []any{"$." + field}
	} else {
		aggExpr = "COUNT(*)"
	}

	if groupBy == "" {
		query := fmt.Sprintf(`SELECT %s FROM %s %s`, aggExpr, table, where)
		args := append(append([]any{}, aggArgs...), whereArgs...)
		var v sql.NullFloat64
		if err := s.read.QueryRow(query, args...).Scan(&v); err != nil {
			return nil, fmt.Errorf("aggregating: %w", err)
		}
		result := AggregateResult{}
		if v.Valid {
			result.Value = &v.Float64
		}
		return []AggregateResult{result}, nil
	}

	query := fmt.Sprintf(`SELECT json_extract(data, ?) AS grp, %s FROM %s %s GROUP BY grp ORDER BY grp`, aggExpr, table, where)
	args := append([]any{"$." + groupBy}, aggArgs...)
	args = append(args, whereArgs...)
	rows, err := s.read.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregating: %w", err)
	}
	defer rows.Close()

	results := []AggregateResult{}
	for rows.Next() {
		var grp sql.NullString
		var v sql.NullFloat64
		if err := rows.Scan(&grp, &v); err != nil {
			return nil, fmt.Errorf("scanning aggregate row: %w", err)
		}
		r := AggregateResult{}
		if grp.Valid {
			r.Group = &grp.String
		}
		if v.Valid {
			r.Value = &v.Float64
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
