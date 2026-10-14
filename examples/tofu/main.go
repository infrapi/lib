// Command tofu is a worked example of pkg/tofu: describing a provider and
// the resources it owns as data, applying that description in one call,
// then destroying it the same way.
//
// It takes the path to a provider plugin executable as its only argument.
// Any provider implementing the "local_file" resource type works; the
// simplest is HashiCorp's terraform-provider-local:
// https://registry.terraform.io/providers/hashicorp/local
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/infrapi/lib/pkg/tofu"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <provider-executable>\n", os.Args[0])
		os.Exit(1)
	}
	exe := os.Args[1]

	fmt.Println("=== InfraPi Tofu Package Examples ===")
	fmt.Println()

	example1(exe)
}

// Example 1: declare one provider and two resources, apply the plan, apply
// it again to show the second run updates in place, then destroy it.
//
// Plan.Providers can list as many providers as a plan needs, each pointing
// at its own executable; this example declares only one because it needs no
// extra provider plugin to run.
func example1(exe string) {
	fmt.Println("--- Example 1: Apply, Re-apply, Destroy ---")

	plan := tofu.Plan{
		Providers: []tofu.ProviderSpec{
			{Name: "local", Executable: exe, TerraformVersion: "1.15.5"},
		},
		Resources: []tofu.ResourceSpec{
			{
				Provider: "local", Address: "local_file.greeting", ResourceType: "local_file",
				Config: map[string]any{"content": "hello", "filename": "./greeting.txt"},
			},
			{
				Provider: "local", Address: "local_file.farewell", ResourceType: "local_file",
				Config: map[string]any{"content": "goodbye", "filename": "./farewell.txt"},
			},
		},
	}

	ctx := context.Background()

	result, err := tofu.Apply(ctx, plan, nil)
	if err != nil {
		log.Fatalf("Apply: %v", err)
	}
	for address, res := range result.Resources {
		fmt.Printf("applied %s: filename=%v\n", address, res.Attributes["filename"])
	}

	// Re-applying against the previous result updates rather than
	// recreates every resource, since both addresses are already present
	// in prior.
	result, err = tofu.Apply(ctx, plan, result.Resources)
	if err != nil {
		log.Fatalf("re-apply: %v", err)
	}
	fmt.Println("re-applied with no changes")

	if err := tofu.Destroy(ctx, plan, result.Resources); err != nil {
		log.Fatalf("Destroy: %v", err)
	}
	fmt.Println("destroyed")
	fmt.Println()
}
