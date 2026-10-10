package model

import (
	"context"
	"errors"

	"github.com/pgplex/pgschema/internal/postgres"
	"github.com/pgplex/pgschema/ir"
)

// Provider is the desired-state provider for a .pgmodel: a third
// implementation of postgres.DesiredStateProvider beside the embedded and the
// external database. It holds the desired IR already, so it starts and
// connects to nothing; GeneratePlan asks it for the IR (DesiredIR) instead of
// applying SQL to it.
type Provider struct {
	doc *Document
}

var _ postgres.DesiredStateProvider = (*Provider)(nil)

// NewProvider reads and validates the .pgmodel at path.
func NewProvider(path string) (*Provider, error) {
	doc, err := Read(path)
	if err != nil {
		return nil, err
	}
	return &Provider{doc: doc}, nil
}

// DesiredIR returns the desired state for schema (see Document.DesiredState).
func (p *Provider) DesiredIR(schema string) (*ir.IR, error) {
	return p.doc.DesiredState(schema)
}

// GetConnectionDetails returns zero values: a model source has no database.
func (p *Provider) GetConnectionDetails() (string, int, string, string, string) {
	return "", 0, "", "", ""
}

// GetSchemaName returns "": there is no temporary schema to inspect.
func (p *Provider) GetSchemaName() string { return "" }

// ApplySchema refuses: a model source has no SQL to apply. GeneratePlan never
// calls it for a Provider.
func (p *Provider) ApplySchema(context.Context, string, string) error {
	return errors.New("a .pgmodel desired state has no SQL to apply")
}

// Stop is a no-op.
func (p *Provider) Stop() error { return nil }
