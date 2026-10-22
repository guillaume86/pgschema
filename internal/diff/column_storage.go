package diff

import (
	"fmt"
	"strings"

	"github.com/pgplex/pgschema/ir"
)

// merp fork: a column's STORAGE planned as ALTER TABLE … ALTER COLUMN … SET
// STORAGE (merp#1028; fork/README.md, patch 5; the field is ir/column_storage.go).

// typeStorage is the default storage of the current column's type: what
// inspection read, else the built-in type's default when the current state is
// a document (ir.TypeDefaultStorage), else "" (unknown).
func typeStorage(old *ir.Column) string {
	if old.TypeStorage != "" {
		return old.TypeStorage
	}
	return ir.TypeDefaultStorage(old.DataType)
}

// canonicalStorage is storage with a name for the type's default read as the
// absent storage, "": the default named explicitly equals the default left out.
func canonicalStorage(storage, typeDefault string) string {
	if storage != "" && storage == typeDefault {
		return ""
	}
	return storage
}

// wantedStorage is the storage the desired column asks of the current one, ""
// for its type's default.
func wantedStorage(old, new *ir.Column) string {
	return canonicalStorage(new.Storage, typeStorage(old))
}

// columnStorageEqual reports whether the current column already has the
// storage the desired one asks for.
func columnStorageEqual(old, new *ir.Column) bool {
	return wantedStorage(old, new) == canonicalStorage(old.Storage, typeStorage(old))
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
	case columnStorageEqual(cd.Old, cd.New):
		return nil
	case want != "":
		target = want
	case typeStorage(cd.Old) != "":
		target = typeStorage(cd.Old)
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
