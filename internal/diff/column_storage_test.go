package diff

import (
	"reflect"
	"testing"

	"github.com/pgplex/pgschema/ir"
)

// merp fork: the column storage diff (merp#1028).

func storageColumn(dataType, storage, typeStorage string) *ir.Column {
	return &ir.Column{Name: "bytes", DataType: dataType, IsNullable: false, Storage: storage, TypeStorage: typeStorage}
}

func TestColumnStorageDiff(t *testing.T) {
	const table = "merp.blob_chunk" // the target schema's own tables are written unqualified
	cases := []struct {
		name     string
		old, new *ir.Column
		want     []string
	}{
		{
			name: "a storage the column lacks is set",
			old:  storageColumn("bytea", "", "extended"),
			new:  storageColumn("bytea", "external", ""),
			want: []string{"ALTER TABLE blob_chunk ALTER COLUMN bytes SET STORAGE EXTERNAL;"},
		},
		{
			name: "a column that has the storage plans nothing",
			old:  storageColumn("bytea", "external", "extended"),
			new:  storageColumn("bytea", "external", ""),
		},
		{
			name: "a storage naming the type's default is the default",
			old:  storageColumn("bytea", "", "extended"),
			new:  storageColumn("bytea", "extended", ""),
		},
		{
			name: "a dropped storage resets the column to its type's default",
			old:  storageColumn("bytea", "external", "extended"),
			new:  storageColumn("bytea", "", ""),
			want: []string{"ALTER TABLE blob_chunk ALTER COLUMN bytes SET STORAGE EXTENDED;"},
		},
		{
			name: "a dropped storage on a document's current state resets to DEFAULT",
			old:  storageColumn("bytea", "external", ""),
			new:  storageColumn("bytea", "", ""),
			want: []string{"ALTER TABLE blob_chunk ALTER COLUMN bytes SET STORAGE DEFAULT;"},
		},
		{
			name: "a storage is set again after a type change resets it",
			old:  storageColumn("text", "external", "extended"),
			new:  storageColumn("bytea", "external", ""),
			want: []string{
				"ALTER TABLE blob_chunk ALTER COLUMN bytes TYPE bytea USING bytes::bytea;",
				"ALTER TABLE blob_chunk ALTER COLUMN bytes SET STORAGE EXTERNAL;",
			},
		},
		{
			name: "a type change alone resets the storage, nothing more",
			old:  storageColumn("text", "external", "extended"),
			new:  storageColumn("bytea", "", ""),
			want: []string{"ALTER TABLE blob_chunk ALTER COLUMN bytes TYPE bytea USING bytes::bytea;"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if equal := columnsEqual(tc.old, tc.new, "merp"); equal != (len(tc.want) == 0) {
				t.Fatalf("columnsEqual = %v, want %v", equal, len(tc.want) == 0)
			}
			if len(tc.want) == 0 {
				return
			}
			got := (&ColumnDiff{Old: tc.old, New: tc.new}).generateColumnSQL("merp", "blob_chunk", "merp")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s:\n got %q\nwant %q", table, got, tc.want)
			}
		})
	}
}
