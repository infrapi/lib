// Command infrapi-contract writes an OpenAPI contract from a resource's short
// description (see package contract):
//
//	infrapi-contract api/bookmarks.yaml api/bookmarks/v1.yaml
//
// Run it from go generate, before oapi-codegen:
//
//	//go:generate go tool infrapi-contract api/bookmarks.yaml api/bookmarks/v1.yaml
//	//go:generate go tool oapi-codegen -config oapi-codegen.yaml api/bookmarks/v1.yaml
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/infrapi/lib/pkg/contract"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "infrapi-contract:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: infrapi-contract <resource.yaml> <contract.yaml>")
	}
	in, out := args[0], args[1]
	data, err := os.ReadFile(in) // #nosec G304 G703 -- the file the developer names, from go generate
	if err != nil {
		return err
	}
	r, err := contract.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	doc, err := contract.Generate(r)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	header := fmt.Sprintf("# Generated from %s by infrapi-contract. DO NOT EDIT: change %s and run go generate.\n",
		filepath.Base(in), filepath.Base(in))
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil { // #nosec G703 -- the developer's own path
		return err
	}
	return os.WriteFile(out, append([]byte(header), doc...), 0o600) // #nosec G306 G703 -- the developer's own source file
}
