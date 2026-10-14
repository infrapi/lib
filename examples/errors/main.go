package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

func main() {
	fmt.Println("=== InfraPi Errors Package Examples ===")
	fmt.Println()

	// Example 1: An error the service raises itself
	example1()

	// Example 2: Wrapping the cause instead of flattening it
	example2()

	// Example 3: Turning any error into an HTTP status code
	example3()

	// Example 4: Answering a request with the error payload
	example4()
}

// Example 1: New, with an optional remediation hint.
func example1() {
	fmt.Println("--- Example 1: Raising an Error ---")

	err := infraerrors.New(http.StatusNotImplemented, "subscription.SetState", "state %q not supported", "paused")
	fmt.Printf("%v\n", err)

	err = err.WithSolution("use one of: active, cancelled")
	fmt.Printf("%v\n", err)

	// a recurring problem deserves a type URI and a title describing the type
	err = err.WithType("https://infrapi.example.com/errors/unsupported-state", "Unsupported subscription state")
	fmt.Printf("%v\n\n", err)
}

// Example 2: Wrap keeps the cause reachable for errors.Is and errors.As.
func example2() {
	fmt.Println("--- Example 2: Wrapping a Cause ---")

	_, cause := os.Open("/does/not/exist")

	// an empty format takes the message of the cause
	err := infraerrors.Wrap(cause, http.StatusBadGateway, "config.Read", "")
	fmt.Printf("%v\n", err)

	err = infraerrors.Wrap(cause, http.StatusBadGateway, "config.Read", "unable to read %s", "app.env")
	fmt.Printf("%v\n", err)

	fmt.Printf("errors.Is(err, os.ErrNotExist) = %t\n\n", errors.Is(err, os.ErrNotExist))
}

// Example 3: ErrorCode is what a handler needs to pick a status.
func example3() {
	fmt.Println("--- Example 3: Status Code of Any Error ---")

	own := infraerrors.New(http.StatusNotFound, "user.Get", "no user with id %d", 42)
	foreign := fmt.Errorf("something nobody identified")

	fmt.Printf("nil            → %d\n", infraerrors.ErrorCode(nil))
	fmt.Printf("own error      → %d\n", infraerrors.ErrorCode(own))
	fmt.Printf("wrapped deeper → %d\n", infraerrors.ErrorCode(fmt.Errorf("handler: %w", own)))
	fmt.Printf("foreign error  → %d\n\n", infraerrors.ErrorCode(foreign))
}

// Example 4: ErrorCast gives the RFC 9457 payload to serialise, whatever came
// back. In a gin handler that is:
//
//	payload, _ := json.Marshal(infraerrors.ErrorCast(err))
//	c.Data(infraerrors.ErrorCode(err), infraerrors.MediaType, payload)
func example4() {
	fmt.Println("--- Example 4: Error Payload ---")
	fmt.Printf("Content-Type: %s\n", infraerrors.MediaType)

	identified := infraerrors.New(http.StatusNotFound, "user.Get", "no user with id %d", 42).
		WithType("https://infrapi.example.com/errors/unknown-user", "Unknown user").
		WithSolution("list the users first")
	identified.Instance = "/users/42"

	for _, err := range []error{
		identified,
		fmt.Errorf("open app.env: %w", os.ErrNotExist),
	} {
		payload, _ := json.Marshal(infraerrors.ErrorCast(err))
		fmt.Printf("%d %s\n", infraerrors.ErrorCode(err), payload)
	}
}
