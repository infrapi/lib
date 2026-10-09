package app

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// complete adds to a contract what every contract shares, unless the contract
// has it already: the error format (the Problem schema, and a default error
// answer on every operation), and the API key every call carries. A contract
// says only what is its own; what it is served, validated and documented as
// has the rest. The author's order and comments are kept.
func complete(content []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return content, nil // not a YAML or JSON object: the loader says what is wrong
	}
	doc := root.Content[0]

	components := child(doc, "components")
	setDefault(child(components, "schemas"), "Problem", problemSchema)
	setDefault(child(components, "responses"), "Problem", problemResponse)
	if lookup(doc, "security") == nil {
		setDefault(child(components, "securitySchemes"), "apikey", `{type: apiKey, in: header, name: apikey}`)
		setDefault(doc, "security", `[{apikey: []}]`)
	}

	if paths := lookup(doc, "paths"); paths != nil && paths.Kind == yaml.MappingNode {
		for i := 1; i < len(paths.Content); i += 2 {
			item := paths.Content[i]
			if item.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j < len(item.Content); j += 2 {
				switch item.Content[j].Value {
				case "get", "put", "post", "delete", "patch", "head", "options", "trace":
					if responses := lookup(item.Content[j+1], "responses"); responses != nil && responses.Kind == yaml.MappingNode {
						setDefault(responses, "default", `{$ref: "#/components/responses/Problem"}`)
					}
				}
			}
		}
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

const problemSchema = `
type: object
description: An error, as an RFC 9457 problem document.
required: [status, code, title]
properties:
  status: {type: integer}
  code: {type: string, description: "Stable and machine readable: not_found, invalid_request, ..."}
  title: {type: string}
  detail: {type: string}
  retryable: {type: boolean, description: Whether the same call may succeed later.}
  correlation_id: {type: string, description: "The call's id, in the service's logs too."}
`

const problemResponse = `
description: An error, as a problem document
content:
  application/problem+json:
    schema: {$ref: "#/components/schemas/Problem"}
`

// lookup is a mapping's value for key, nil when absent.
func lookup(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// child is a mapping's mapping under key, added when absent.
func child(m *yaml.Node, key string) *yaml.Node {
	if v := lookup(m, key); v != nil {
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return v
}

// setDefault sets key to the YAML value, unless the mapping has it.
func setDefault(m *yaml.Node, key, value string) {
	if m == nil || m.Kind != yaml.MappingNode || lookup(m, key) != nil {
		return
	}
	var v yaml.Node
	if err := yaml.Unmarshal([]byte(value), &v); err != nil || len(v.Content) == 0 {
		panic(fmt.Sprintf("app: the library's own YAML does not parse: %v", err)) // a constant of this file
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v.Content[0])
}
