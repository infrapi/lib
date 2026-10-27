package tofu

import (
	"errors"
	"net/http"
	"strings"

	"github.com/opentofu/provider-client/tofuprovider/providerops"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// Phase names the step of the provider protocol a provider's diagnostics
// came from.
type Phase string

const (
	// PhaseSchema is fetching the provider's schema, in [Start].
	PhaseSchema Phase = "schema"
	// PhaseValidate is validating a provider or resource configuration.
	PhaseValidate Phase = "validate"
	// PhaseConfigure is configuring the provider, once validated.
	PhaseConfigure Phase = "configure"
	// PhasePlan is planning a create or an update.
	PhasePlan Phase = "plan"
	// PhaseApply is applying a planned create or update.
	PhaseApply Phase = "apply"
	// PhaseRead is reading a resource's current state.
	PhaseRead Phase = "read"
	// PhaseImport is turning an id into a resource's state.
	PhaseImport Phase = "import"
	// PhaseDelete is planning or applying a delete.
	PhaseDelete Phase = "delete"
)

// Diagnostic is one error-severity diagnostic a provider reported.
type Diagnostic struct {
	Summary string
	Detail  string
}

// DiagnosticsError carries the error diagnostics a provider reported, and
// the step that produced them. Every error this package returns for a
// provider's diagnostics wraps one: find it with [errors.As].
type DiagnosticsError struct {
	Phase       Phase
	Diagnostics []Diagnostic
}

func (e *DiagnosticsError) Error() string {
	summaries := make([]string, len(e.Diagnostics))
	for i, d := range e.Diagnostics {
		summaries[i] = d.Summary
	}
	return string(e.Phase) + ": " + strings.Join(summaries, "; ")
}

// ErrReplaceRequired is what [Provider.Update] returns, wrapped, when the
// change it was given cannot be made in place: the resource has to be
// deleted and created again. Test for it with [errors.Is].
var ErrReplaceRequired = errors.New("the change requires replacing the resource")

// diagnosticsError turns provider diagnostics into an [infraerrors.Error]
// when they include at least one error-severity diagnostic, and returns nil
// otherwise. Warning diagnostics are dropped: this package has no logger to
// send them to, so a caller that needs them should call the provider
// methods directly instead of going through this package.
//
// Diagnostics from validating a configuration or planning a change describe
// a configuration the provider refuses: they carry
// [http.StatusUnprocessableEntity], the caller's to fix. Any other step
// failing is a problem between the provider and what it manages:
// [http.StatusBadGateway], the fault is downstream of the caller. The
// diagnostics themselves are in the wrapped [DiagnosticsError].
func diagnosticsError(origin string, phase Phase, diags providerops.Diagnostics) error {
	if !diags.HasErrors() {
		return nil
	}

	de := &DiagnosticsError{Phase: phase}
	var summaries, details []string
	for d := range diags.All() {
		if d.Severity() != providerops.DiagnosticError {
			continue
		}
		de.Diagnostics = append(de.Diagnostics, Diagnostic{Summary: d.Summary(), Detail: d.Detail()})
		summaries = append(summaries, d.Summary())
		if detail := d.Detail(); detail != "" {
			details = append(details, detail)
		}
	}

	var err *infraerrors.Error
	switch phase {
	case PhaseValidate, PhasePlan:
		err = infraerrors.New(http.StatusUnprocessableEntity, origin, "%s", strings.Join(summaries, "; ")).
			WithSlug("invalid_configuration").WithTitle("Invalid configuration")
	default:
		err = infraerrors.New(http.StatusBadGateway, origin, "%s", strings.Join(summaries, "; ")).
			WithSlug("upstream_error").WithTitle("Upstream error")
	}
	err.Err = de
	if len(details) > 0 {
		err = err.WithSolution("%s", strings.Join(details, " "))
	}
	return err
}
