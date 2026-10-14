package tofu

import (
	"net/http"
	"strings"

	"github.com/opentofu/provider-client/tofuprovider/providerops"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// diagnosticsError turns provider diagnostics into an [infraerrors.Error]
// when they include at least one error-severity diagnostic, and returns nil
// otherwise. Warning diagnostics are dropped: this package has no logger to
// send them to, so a caller that needs them should call the provider
// methods directly instead of going through this package.
//
// A provider's diagnostics describe a problem in the request this package
// sent it, or in the provider's own connection to whatever it manages, so
// the result carries [http.StatusBadGateway]: the fault is downstream of the
// caller.
func diagnosticsError(origin string, diags providerops.Diagnostics) error {
	if !diags.HasErrors() {
		return nil
	}

	var summaries, details []string
	for d := range diags.All() {
		if d.Severity() != providerops.DiagnosticError {
			continue
		}
		summaries = append(summaries, d.Summary())
		if detail := d.Detail(); detail != "" {
			details = append(details, detail)
		}
	}

	err := infraerrors.New(http.StatusBadGateway, origin, "%s", strings.Join(summaries, "; "))
	if len(details) > 0 {
		err = err.WithSolution("%s", strings.Join(details, " "))
	}
	return err
}
