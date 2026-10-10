package ir

// NormalizeModel applies the inspector's normalization to an IR that was not
// inspected (a .pgmodel desired state, internal/model) so it compares with an
// inspected current state, and derives the fields the IR does not serialize
// (IsSerial). merp fork: see fork/README.md.
func NormalizeModel(model *IR) {
	normalizeIR(model)
}
