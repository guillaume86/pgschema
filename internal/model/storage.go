package model

import (
	"fmt"
	"strings"

	"github.com/pgplex/pgschema/ir"
)

// normalizeStorage lower-cases every column's storage and refuses a word
// PostgreSQL's SET STORAGE does not take (merp#1028; fork/README.md, patch 5).
func normalizeStorage(s *ir.Schema) error {
	for _, t := range s.Tables {
		for _, c := range t.Columns {
			if c.Storage == "" {
				continue
			}
			c.Storage = strings.ToLower(c.Storage)
			if !ir.IsStorageName(c.Storage) {
				return fmt.Errorf("invalid .pgmodel model: column %s.%s: storage %q is not plain, external, extended or main", t.Name, c.Name, c.Storage)
			}
		}
	}
	return nil
}
