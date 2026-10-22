package ir

import (
	"context"
	"fmt"
	"strings"
)

// merp fork: a column's STORAGE (merp#1028; fork/README.md, patch 5).
//
// Column.Storage is the column's storage strategy when it is not its data
// type's default: "plain", "external", "extended" or "main", the words of
// ALTER TABLE … ALTER COLUMN … SET STORAGE. Empty means the type's default, so
// a database that never set a storage inspects as it always did. TypeStorage,
// set by inspection only and never serialized, is that default: the diff
// resets a column to it when the desired state drops its storage.

// StorageNames maps pg_attribute.attstorage and pg_type.typstorage codes to
// the words Column.Storage holds.
var StorageNames = map[string]string{
	"p": "plain",
	"e": "external",
	"x": "extended",
	"m": "main",
}

// builtinTypeStorage is pg_type.typstorage of the common built-in types, as
// Column.DataType names them, for a current state read from a document, which
// carries no TypeStorage.
var builtinTypeStorage = map[string]string{
	"bytea": "extended", "text": "extended", "character varying": "extended", "varchar": "extended",
	"character": "extended", "json": "extended", "jsonb": "extended", "xml": "extended", "tsvector": "extended",
	"numeric": "main", "inet": "main", "cidr": "main",
	"uuid": "plain", "boolean": "plain", "smallint": "plain", "integer": "plain", "bigint": "plain",
	"real": "plain", "double precision": "plain", "date": "plain", "time without time zone": "plain",
	"timestamp without time zone": "plain", "timestamp with time zone": "plain", "interval": "plain",
}

// TypeDefaultStorage is the default storage of a built-in data type, every
// array's included (extended), or "" for a type it does not know.
func TypeDefaultStorage(dataType string) string {
	if strings.HasSuffix(dataType, "[]") {
		return "extended"
	}
	return builtinTypeStorage[dataType]
}

// IsStorageName reports whether name is one of the words Column.Storage holds.
func IsStorageName(name string) bool {
	for _, n := range StorageNames {
		if n == name {
			return true
		}
	}
	return false
}

const columnStorageQuery = `
SELECT c.relname, a.attname, a.attstorage::text, t.typstorage::text
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped`

// buildColumnStorage reads every inspected column's storage and its type's
// default. It runs once the columns are built, outside the concurrent groups.
func (i *Inspector) buildColumnStorage(ctx context.Context, schema *IR, targetSchema string) error {
	s, ok := schema.Schemas[targetSchema]
	if !ok || s == nil {
		return nil
	}
	rows, err := i.db.QueryContext(ctx, columnStorageQuery, targetSchema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table, column, storage, typeStorage string
		if err := rows.Scan(&table, &column, &storage, &typeStorage); err != nil {
			return err
		}
		t, ok := s.Tables[table]
		if !ok {
			continue
		}
		for _, c := range t.Columns {
			if c.Name != column {
				continue
			}
			name, known := StorageNames[storage]
			if !known {
				return fmt.Errorf("column %s.%s: unknown storage %q", table, column, storage)
			}
			c.TypeStorage = StorageNames[typeStorage]
			if storage != typeStorage {
				c.Storage = name
			}
			break
		}
	}
	return rows.Err()
}
