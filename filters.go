package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// filterOps maps the operator suffix a client writes (field__op=value) to
// the literal SQL operator used in the generated query. sqlOp always comes
// from this map — never straight from request input — so only these fixed
// strings can ever end up in SQL text; user-controlled values only ever
// reach the query as bound parameters.
var filterOps = map[string]string{
	"eq":   "=",
	"ne":   "!=",
	"lt":   "<",
	"lte":  "<=",
	"gt":   ">",
	"gte":  ">=",
	"like": "LIKE",
}

type filterCondition struct {
	field string
	sqlOp string
	value any
}

// reservedQueryParams are handled separately by the caller and never
// treated as data filters.
var reservedQueryParams = map[string]bool{
	"limit": true, "offset": true, "q": true, "rerank": true, "sort": true,
	"op": true, "field": true, "group_by": true,
}

// parseFilters turns query params other than the reserved ones into
// conditions on top-level fields of a collection item's data. A param name
// is either "field" (implicit eq) or "field__op" (op must be a key in
// filterOps). Field names are checked against collectionNameRe — not for
// injection safety (json_extract's path argument is always bound as an
// ordinary parameter, never concatenated into SQL text, so there's nothing
// to inject) but so a malformed filter fails loudly with 400 instead of
// silently matching nothing.
func parseFilters(query url.Values) ([]filterCondition, error) {
	var conds []filterCondition
	for key, values := range query {
		if reservedQueryParams[key] {
			continue
		}
		field, op := key, "eq"
		if idx := strings.LastIndex(key, "__"); idx > 0 {
			field, op = key[:idx], key[idx+2:]
		}
		if !collectionNameRe.MatchString(field) {
			return nil, fmt.Errorf("invalid filter field %q", field)
		}
		sqlOp, ok := filterOps[op]
		if !ok {
			return nil, fmt.Errorf("unknown filter operator %q on field %q (allowed: eq, ne, lt, lte, gt, gte, like)", op, field)
		}
		for _, raw := range values {
			conds = append(conds, filterCondition{field: field, sqlOp: sqlOp, value: parseFilterValue(raw)})
		}
	}
	return conds, nil
}

// parseFilterValue converts a raw query string into the Go type SQLite
// should compare against. json_extract returns numeric/boolean JSON values
// with SQLite's native numeric affinity, not as text, so a filter value of
// "50" has to be bound as an integer (or "true" as a bool) to compare
// equal — binding it as the string "50" would silently never match.
func parseFilterValue(raw string) any {
	if raw == "true" {
		return true
	}
	if raw == "false" {
		return false
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f
	}
	return raw
}

// filterWhereSQL renders conditions as "json_extract(data, ?) op ? AND ..."
// plus the matching bind args in (path, value, path, value, ...) order.
func filterWhereSQL(conds []filterCondition) (string, []any) {
	if len(conds) == 0 {
		return "", nil
	}
	parts := make([]string, len(conds))
	args := make([]any, 0, len(conds)*2)
	for i, c := range conds {
		parts[i] = fmt.Sprintf("json_extract(data, ?) %s ?", c.sqlOp)
		args = append(args, "$."+c.field, c.value)
	}
	return strings.Join(parts, " AND "), args
}

// sortSpec is a request to order results by a top-level field of data
// instead of the caller's default (id for list, distance for search).
type sortSpec struct {
	field string
	desc  bool
}

// parseSort reads the "sort" query param: "campo" for ascending, "-campo"
// for descending. Returns nil if absent — the caller's default ordering
// applies.
func parseSort(query url.Values) (*sortSpec, error) {
	raw := query.Get("sort")
	if raw == "" {
		return nil, nil
	}
	field := raw
	desc := false
	if strings.HasPrefix(raw, "-") {
		field = raw[1:]
		desc = true
	}
	if !collectionNameRe.MatchString(field) {
		return nil, fmt.Errorf("invalid sort field %q", field)
	}
	return &sortSpec{field: field, desc: desc}, nil
}

// orderBySQL renders "json_extract(data, ?) ASC|DESC" and its bind arg.
// ASC/DESC is always one of these two literal strings from s.desc — never
// user input — since SQL doesn't allow parameterizing sort direction.
func (s *sortSpec) orderBySQL() (string, []any) {
	dir := "ASC"
	if s.desc {
		dir = "DESC"
	}
	return fmt.Sprintf("json_extract(data, ?) %s", dir), []any{"$." + s.field}
}
