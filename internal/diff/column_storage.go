package diff

import (
	"fmt"
	"strings"

	"github.com/pgplex/pgschema/ir"
)

// merp fork: a column's STORAGE planned as ALTER TABLE … ALTER COLUMN … SET
// STORAGE (merp#1028; fork/README.md, patch 5; the field is ir/column_storage.go).

// wantedStorage is the storage the desired column asks of the current one, ""
// for its type's default: a desired storage that names the current type's
// default asks for nothing, since inspection reports only a storage that
// differs from it.
func wantedStorage(old, new *ir.Column) string {
	if new.Storage != "" && new.Storage == old.TypeStorage {
		return ""
	}
	return new.Storage
}

// columnStorageEqual reports whether the current column already has the
// storage the desired one asks for.
func columnStorageEqual(old, new *ir.Column) bool {
	return wantedStorage(old, new) == old.Storage
}

// columnStorageSQL is the SET STORAGE a modified column needs, after its other
// clauses. ALTER COLUMN TYPE (a type or a collation change) resets the storage
// to the new type's default, so a desired storage is set again after it. A
// desired state that drops the storage resets the column to its type's default,
// named when inspection read it (every PostgreSQL), DEFAULT otherwise
// (PostgreSQL 16+, a current state read from a document).
func columnStorageSQL(cd *ColumnDiff, qualifiedTableName, targetSchema string) []string {
	typeChanged := stripSchemaPrefix(comparableColumnType(cd.Old), targetSchema) != stripSchemaPrefix(comparableColumnType(cd.New), targetSchema) ||
		stripSchemaPrefix(cd.Old.Collation, targetSchema) != stripSchemaPrefix(cd.New.Collation, targetSchema)
	want := wantedStorage(cd.Old, cd.New)
	var target string
	switch {
	case typeChanged:
		target = cd.New.Storage
	case want == cd.Old.Storage:
		return nil
	case want != "":
		target = want
	case cd.Old.TypeStorage != "":
		target = cd.Old.TypeStorage
	default:
		target = "default"
	}
	if target == "" {
		return nil
	}
	return []string{setStorageSQL(qualifiedTableName, cd.New.Name, target)}
}

// collectColumnStorageSQL sets the storage of the columns a CREATE TABLE or an
// ADD COLUMN just created, each one that names a storage.
func collectColumnStorageSQL(schema, table string, columns []*ir.Column, targetSchema string, collector *diffCollector) {
	tableName := qualifyEntityNameMode(schema, table, targetSchema, collector.qualifySchema)
	for _, column := range columns {
		if column.Storage == "" {
			continue
		}
		context := &diffContext{
			Type:                DiffTypeTableColumn,
			Operation:           DiffOperationAlter,
			Path:                fmt.Sprintf("%s.%s.%s", schema, table, column.Name),
			Source:              column,
			CanRunInTransaction: true,
		}
		collector.collect(context, setStorageSQL(tableName, column.Name, column.Storage))
	}
}

func setStorageSQL(qualifiedTableName, column, storage string) string {
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET STORAGE %s;",
		qualifiedTableName, ir.QuoteIdentifier(column), strings.ToUpper(storage))
}
