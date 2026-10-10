// Package model reads a .pgmodel document: a desired state given as pgschema's
// IR serialized to JSON. plan and apply given --file x.pgmodel load it straight
// into the differ, so no embedded Postgres is started to materialize it.
//
// This package is patch 1 of the merp fork (see fork/README.md). Everything the
// patch needs lives here; upstream files only carry the hooks listed there.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pgplex/pgschema/ir"
)

// SpecVersion is the .pgmodel document version this build reads and writes.
// Only the major part is enforced: a reader accepts any 1.x document.
const SpecVersion = "1.0"

// Document is a .pgmodel: the desired state (Model) plus the envelope fields
// erp emits. Refactor (rename directives) is not supported by this build and
// is refused when non-empty. Config is informational: scope comes from the
// invocation's --schema, as for --file, and is never re-applied from here.
type Document struct {
	SpecVersion string            `json:"specVersion"`
	Model       json.RawMessage   `json:"model"`
	Refactor    []json.RawMessage `json:"refactor,omitempty"`
	Config      *Config           `json:"config,omitempty"`
}

// Config records the scope the document was produced for. It is kept for
// round-trip fidelity and never changes where or how a plan runs.
type Config struct {
	Schema string          `json:"schema,omitempty"`
	Ignore json.RawMessage `json:"ignore,omitempty"`
}

// Extension marks a --file path as a .pgmodel document.
const Extension = ".pgmodel"

// IsDocumentPath reports whether path names a .pgmodel document, which plan
// and apply read with the model provider instead of applying it as SQL.
func IsDocumentPath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), Extension)
}

// Read loads and validates the .pgmodel document at path.
func Read(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading .pgmodel: %w", err)
	}
	doc, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

// Parse decodes and validates a .pgmodel document. Envelope and model are
// decoded strictly: an unknown field is an error, so a misspelled key (say
// "refactors") cannot drop rename directives, and a document written for
// another IR shape fails here rather than planning against a silently
// truncated desired state.
func Parse(data []byte) (*Document, error) {
	var doc Document
	if err := decodeStrict(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid .pgmodel: %w", err)
	}
	if doc.SpecVersion == "" {
		return nil, errors.New("invalid .pgmodel: missing specVersion")
	}
	if majorOf(doc.SpecVersion) != majorOf(SpecVersion) {
		return nil, fmt.Errorf("unsupported .pgmodel specVersion %q: this build supports %s", doc.SpecVersion, SpecVersion)
	}
	if len(doc.Model) == 0 || bytes.Equal(bytes.TrimSpace(doc.Model), []byte("null")) {
		return nil, errors.New("invalid .pgmodel: missing model")
	}
	if len(doc.Refactor) > 0 {
		return nil, fmt.Errorf("invalid .pgmodel: %d refactor entries, but rename directives are not supported in this build", len(doc.Refactor))
	}
	if _, err := doc.decodeModel(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// DesiredState returns the desired state for schema: a fresh IR holding only
// that schema, normalized the way the inspector normalizes an inspected
// database, so it compares with the current state as an inspected desired
// state would. The model must declare schema.
func (d *Document) DesiredState(schema string) (*ir.IR, error) {
	model, err := d.decodeModel()
	if err != nil {
		return nil, err
	}
	s, ok := model.Schemas[schema]
	if !ok || s == nil {
		return nil, fmt.Errorf(".pgmodel declares no schema %q (it declares: %s); pass the schema it was produced for with --schema", schema, declared(model))
	}
	desired := &ir.IR{Metadata: model.Metadata, Schemas: map[string]*ir.Schema{schema: s}}
	fillMaps(s)
	ir.NormalizeModel(desired)
	return desired, nil
}

func (d *Document) decodeModel() (*ir.IR, error) {
	var model ir.IR
	if err := decodeStrict(d.Model, &model); err != nil {
		return nil, fmt.Errorf("invalid .pgmodel model: %w", err)
	}
	return &model, nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// fillMaps gives every map the inspector always allocates a non-nil value, so
// a document that omits an empty collection (or writes null) reads like an
// inspected schema.
func fillMaps(s *ir.Schema) {
	if s.Tables == nil {
		s.Tables = map[string]*ir.Table{}
	}
	if s.Views == nil {
		s.Views = map[string]*ir.View{}
	}
	if s.Functions == nil {
		s.Functions = map[string]*ir.Function{}
	}
	if s.Procedures == nil {
		s.Procedures = map[string]*ir.Procedure{}
	}
	if s.Aggregates == nil {
		s.Aggregates = map[string]*ir.Aggregate{}
	}
	if s.Sequences == nil {
		s.Sequences = map[string]*ir.Sequence{}
	}
	if s.Types == nil {
		s.Types = map[string]*ir.Type{}
	}
	for _, t := range s.Tables {
		if t.Constraints == nil {
			t.Constraints = map[string]*ir.Constraint{}
		}
		if t.Indexes == nil {
			t.Indexes = map[string]*ir.Index{}
		}
		if t.Triggers == nil {
			t.Triggers = map[string]*ir.Trigger{}
		}
		if t.Policies == nil {
			t.Policies = map[string]*ir.RLSPolicy{}
		}
	}
}

func declared(model *ir.IR) string {
	if len(model.Schemas) == 0 {
		return "none"
	}
	names := make([]string, 0, len(model.Schemas))
	for name := range model.Schemas {
		names = append(names, fmt.Sprintf("%q", name))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func majorOf(version string) string {
	major, _, _ := strings.Cut(version, ".")
	return major
}
