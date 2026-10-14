package tofu

import (
	"encoding/json"
	"fmt"

	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/opentofu/provider-client/tofuprovider/providerschema"
)

// schemaType computes the cty.Type implied by a provider schema block: an
// object type with one attribute per schema attribute and per nested block
// type, recursively. This mirrors how OpenTofu itself interprets a schema
// wire message.
func schemaType(block providerschema.BlockType) (cty.Type, error) {
	attrTypes := map[string]cty.Type{}

	for name, attr := range block.Attributes() {
		ty, err := attributeType(attr)
		if err != nil {
			return cty.NilType, fmt.Errorf("attribute %q: %w", name, err)
		}
		attrTypes[name] = ty
	}

	for name, nested := range block.NestedBlockTypes() {
		nestedTy, err := schemaType(nested)
		if err != nil {
			return cty.NilType, fmt.Errorf("block %q: %w", name, err)
		}
		wrapped, err := nestTypeByMode(nestedTy, nested.Nesting())
		if err != nil {
			return cty.NilType, fmt.Errorf("block %q: %w", name, err)
		}
		attrTypes[name] = wrapped
	}

	return cty.Object(attrTypes), nil
}

func attributeType(attr providerschema.Attribute) (cty.Type, error) {
	nested := attr.NestedType()
	if nested == nil {
		return attr.Type().AsCtyType()
	}

	attrTypes := map[string]cty.Type{}
	for name, a := range nested.Attributes() {
		ty, err := attributeType(a)
		if err != nil {
			return cty.NilType, fmt.Errorf("attribute %q: %w", name, err)
		}
		attrTypes[name] = ty
	}

	return nestTypeByMode(cty.Object(attrTypes), nested.Nesting())
}

func nestTypeByMode(obj cty.Type, nesting providerschema.NestingMode) (cty.Type, error) {
	switch nesting {
	case providerschema.NestingSingle, providerschema.NestingGroup:
		return obj, nil
	case providerschema.NestingList:
		return cty.List(obj), nil
	case providerschema.NestingSet:
		return cty.Set(obj), nil
	case providerschema.NestingMap:
		return cty.Map(obj), nil
	default:
		return cty.NilType, fmt.Errorf("unsupported nesting mode %s", nesting)
	}
}

// valueFromGo converts a plain Go value (nil, a map[string]any, a []any, or
// any scalar encoding/json can produce) into a cty.Value of type ty, going
// through JSON so that every collection, object and nesting shape a schema
// type can describe is handled the same way encoding/json already handles
// it, without a bespoke reflection-based converter for each cty kind.
func valueFromGo(ty cty.Type, data any) (cty.Value, error) {
	if data == nil {
		return cty.NullVal(ty), nil
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return cty.NilVal, fmt.Errorf("encode value: %w", err)
	}

	v, err := ctyjson.Unmarshal(raw, ty)
	if err != nil {
		return cty.NilVal, fmt.Errorf("value does not conform to %s: %w", ty.FriendlyName(), err)
	}
	return v, nil
}

// valueToGo converts a fully-known cty.Value of type ty back into a plain
// Go map, the inverse of valueFromGo. The value must not contain any
// unknown placeholders; only decode a provider's final, applied state with
// this, never a plan preview.
func valueToGo(v cty.Value, ty cty.Type) (map[string]any, error) {
	raw, err := ctyjson.Marshal(v, ty)
	if err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode value: %w", err)
	}
	return out, nil
}

// proposeNewState emulates the small piece of OpenTofu core business logic
// a caller must reproduce to populate a plan request's proposed new state:
// for each attribute and nested block, use the config value if one was set;
// otherwise a purely computed attribute carries forward its prior value if
// there is one (an update), or is left "unknown" if there is not (a
// create), and anything else is null. Carrying the prior value forward
// matters because most resource types have no plan modifier that would
// otherwise restore it once the caller has proposed "unknown", so a naive
// "always unknown" proposal makes every update look like a full replacement
// to providers that never re-derive computed attributes outside of Create.
//
// Nested blocks and nested-attribute objects are carried over as a single
// opaque unit rather than merged field by field, so a computed attribute
// nested inside one is not individually defaulted this way. That matches
// this package's scope as a thin, generic layer over the protocol; most
// resource types have no computed fields below their top level.
func proposeNewState(block providerschema.BlockType, ty cty.Type, priorState, config cty.Value) cty.Value {
	var configAttrs, priorAttrs map[string]cty.Value
	if !config.IsNull() {
		configAttrs = config.AsValueMap()
	}
	if !priorState.IsNull() {
		priorAttrs = priorState.AsValueMap()
	}

	vals := map[string]cty.Value{}
	for name, attr := range block.Attributes() {
		attrTy := ty.AttributeType(name)
		if v, ok := configAttrs[name]; ok && !v.IsNull() {
			vals[name] = v
			continue
		}
		switch attr.Usage() {
		case providerschema.AttributeComputed, providerschema.AttributeOptionalComputed:
			if pv, ok := priorAttrs[name]; ok {
				vals[name] = pv
			} else {
				vals[name] = cty.UnknownVal(attrTy)
			}
		default:
			vals[name] = cty.NullVal(attrTy)
		}
	}

	for name := range block.NestedBlockTypes() {
		attrTy := ty.AttributeType(name)
		if v, ok := configAttrs[name]; ok && !v.IsNull() {
			vals[name] = v
			continue
		}
		vals[name] = cty.NullVal(attrTy)
	}

	return cty.ObjectVal(vals)
}
