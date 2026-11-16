package tofu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// Action is what applying a [ResourcePlan] would do.
type Action string

const (
	// ActionNone: the resource already matches the configuration.
	ActionNone Action = "none"
	// ActionCreate: there is no resource yet; applying creates it.
	ActionCreate Action = "create"
	// ActionUpdate: the resource differs; applying changes it in place.
	ActionUpdate Action = "update"
	// ActionReplace: the resource differs in a way the provider cannot
	// change in place; applying means deleting it and creating it again.
	ActionReplace Action = "replace"
)

// ResourcePlan is what [Provider.Plan] found: the action applying the
// configuration would take, and the changes it would make.
type ResourcePlan struct {
	Action Action
	// Changes lists the attributes whose value would change, sorted by
	// path.
	Changes []Change
	// Planned is the state the provider plans, as [Resource.Attributes]
	// holds one, with the values only applying will know left null.
	Planned map[string]any
	// Unknown lists the paths of those values, sorted.
	Unknown []string
}

// Change is one attribute a [ResourcePlan] changes.
type Change struct {
	// Path names the attribute: "title", "settings.theme",
	// "permission[0].role", `labels["env"]`.
	Path string
	// Before and After are the value now and once applied, JSON-shaped as
	// [Resource.Attributes] values; both nil when Sensitive, After nil when
	// Unknown.
	Before any
	After  any
	// Sensitive is set for an attribute the schema marks sensitive: its
	// values are left out.
	Sensitive bool
	// Unknown is set when the value is only known once applied.
	Unknown bool
}

// Plan asks the provider what applying config would do, as [Provider.Update]
// would for state, or [Provider.Create] when state holds no attributes, and
// applies nothing: the "terraform plan" of one resource. State is the
// resource as it is now, as [Provider.Read] or [Provider.Import] returns it;
// the changes are between that and the provider's plan, so they include
// whatever was changed outside of this package. A plan that needs
// replacing the resource is an [ActionReplace], not an error as for Update.
//
// The replacement is inferred as Update infers it: from the id becoming
// unknown. A resource type whose plan keeps its id while it replaces the
// resource (a plugin framework resource with UseStateForUnknown, such as
// random_string or random_password) plans an [ActionUpdate].
func (p *Provider) Plan(ctx context.Context, state Resource, config map[string]any) (ResourcePlan, error) {
	origin := "tofu.Provider.Plan(" + p.name + "." + state.ResourceType + ")"

	rt, err := p.resourceTypeByName(origin, state.ResourceType)
	if err != nil {
		return ResourcePlan{}, err
	}
	block, err := blockFrom(rt.schema)
	if err != nil {
		return ResourcePlan{}, infraerrors.Wrap(err, http.StatusBadGateway, origin,
			"resource type %q of provider %q has a schema this client cannot describe", state.ResourceType, p.name)
	}

	priorVal := cty.NullVal(rt.ty)
	if state.Attributes != nil {
		if priorVal, err = valueFromGo(rt.ty, state.Attributes); err != nil {
			return ResourcePlan{}, infraerrors.Wrap(err, http.StatusUnprocessableEntity, origin, "invalid current state for resource type %q", state.ResourceType)
		}
	}

	pl, err := p.plan(ctx, origin, rt, priorVal, state.Internal, config)
	if err != nil {
		return ResourcePlan{}, err
	}

	changes, unknown, err := diff(block, priorVal, pl.planned)
	if err != nil {
		return ResourcePlan{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "compare the plan of resource type %q", state.ResourceType)
	}
	planned, err := valueToGo(cty.UnknownAsNull(pl.planned), rt.ty)
	if err != nil {
		return ResourcePlan{}, infraerrors.Wrap(err, http.StatusBadGateway, origin, "decode plan for resource type %q", state.ResourceType)
	}

	out := ResourcePlan{Changes: changes, Planned: planned, Unknown: unknown}
	switch {
	case priorVal.IsNull():
		out.Action = ActionCreate
	case replacing(priorVal, pl.planned):
		out.Action = ActionReplace
	case len(changes) == 0:
		out.Action = ActionNone
	default:
		out.Action = ActionUpdate
	}
	return out, nil
}

// diff compares a resource's state before and after a plan, attribute by
// attribute, following its schema for what is sensitive. It returns the
// changes and the paths whose planned value is unknown, both sorted.
func diff(block Block, before, after cty.Value) ([]Change, []string, error) {
	d := differ{}
	if err := d.value("", &block, false, before, after); err != nil {
		return nil, nil, err
	}
	sort.Slice(d.changes, func(i, j int) bool { return d.changes[i].Path < d.changes[j].Path })
	sort.Strings(d.unknown)
	return d.changes, d.unknown, nil
}

type differ struct {
	changes []Change
	unknown []string
}

// value compares one value. block describes it when it is an object of the
// schema, nil otherwise; sensitive is set below a sensitive attribute.
func (d *differ) value(path string, block *Block, sensitive bool, before, after cty.Value) error {
	if !after.IsKnown() {
		d.unknown = append(d.unknown, path)
		if !before.IsNull() {
			d.changes = append(d.changes, Change{Path: path, Before: hide(sensitive, before), Unknown: true, Sensitive: sensitive})
		}
		return nil
	}
	if before.IsKnown() && before.RawEquals(after) {
		return nil
	}

	ty := after.Type()
	switch {
	case ty.IsObjectType() && !after.IsNull():
		return d.object(path, block, sensitive, before, after)
	case (ty.IsListType() || ty.IsTupleType()) && !after.IsNull() && !before.IsNull():
		return d.list(path, block, sensitive, before, after)
	case ty.IsMapType() && !after.IsNull() && !before.IsNull():
		return d.mapping(path, block, sensitive, before, after)
	}
	return d.leaf(path, sensitive, before, after)
}

// object compares an object attribute by attribute; a null before is an
// object whose attributes are all null, as for a create.
func (d *differ) object(path string, block *Block, sensitive bool, before, after cty.Value) error {
	names := make([]string, 0, len(after.Type().AttributeTypes()))
	for name := range after.Type().AttributeTypes() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b := cty.NullVal(after.Type().AttributeType(name))
		if !before.IsNull() && before.Type().IsObjectType() && before.Type().HasAttribute(name) {
			b = before.GetAttr(name)
		}
		child, childSensitive := describe(block, name)
		if err := d.value(join(path, name), child, sensitive || childSensitive, b, after.GetAttr(name)); err != nil {
			return err
		}
	}
	return nil
}

// list compares a list index by index: an index past one end is null on
// that side.
func (d *differ) list(path string, block *Block, sensitive bool, before, after cty.Value) error {
	if !before.IsWhollyKnown() {
		return d.leaf(path, sensitive, before, after)
	}
	b, a := before.AsValueSlice(), after.AsValueSlice()
	for i := 0; i < max(len(b), len(a)); i++ {
		bi, ai := elem(b, i, after), elem(a, i, before)
		if err := d.value(path+"["+strconv.Itoa(i)+"]", block, sensitive, bi, ai); err != nil {
			return err
		}
	}
	return nil
}

// mapping compares a map key by key.
func (d *differ) mapping(path string, block *Block, sensitive bool, before, after cty.Value) error {
	if !before.IsWhollyKnown() {
		return d.leaf(path, sensitive, before, after)
	}
	b, a := before.AsValueMap(), after.AsValueMap()
	keys := map[string]bool{}
	for k := range b {
		keys[k] = true
	}
	for k := range a {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	elemTy := after.Type().ElementType()
	for _, k := range sorted {
		bk, ok := b[k]
		if !ok {
			bk = cty.NullVal(elemTy)
		}
		ak, ok := a[k]
		if !ok {
			ak = cty.NullVal(elemTy)
		}
		if err := d.value(path+"["+strconv.Quote(k)+"]", block, sensitive, bk, ak); err != nil {
			return err
		}
	}
	return nil
}

// leaf records a value changed as a whole: a primitive, a set, or a
// collection that appears or disappears.
func (d *differ) leaf(path string, sensitive bool, before, after cty.Value) error {
	c := Change{Path: path, Sensitive: sensitive}
	if !after.IsWhollyKnown() {
		c.Unknown = true
		d.unknown = append(d.unknown, path)
	}
	if !sensitive {
		var err error
		if c.Before, err = toGo(before); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !c.Unknown {
			if c.After, err = toGo(after); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	d.changes = append(d.changes, c)
	return nil
}

// describe finds, in the schema block of an object, the attribute or nested
// block name: the block describing its value, if an object, and whether it
// is sensitive. A nil block describes nothing.
func describe(block *Block, name string) (*Block, bool) {
	if block == nil {
		return nil, false
	}
	if a, ok := block.Attributes[name]; ok {
		if a.Nested != nil {
			return &a.Nested.Block, a.Sensitive
		}
		return nil, a.Sensitive
	}
	if nb, ok := block.Blocks[name]; ok {
		return &nb.Block, false
	}
	return nil, false
}

// hide is v as a Go value, or nil when sensitive or not convertible.
func hide(sensitive bool, v cty.Value) any {
	if sensitive {
		return nil
	}
	g, err := toGo(v)
	if err != nil {
		return nil
	}
	return g
}

// toGo converts any known cty value to a JSON-shaped Go value.
func toGo(v cty.Value) (any, error) {
	if v.IsNull() {
		return nil, nil
	}
	raw, err := ctyjson.Marshal(cty.UnknownAsNull(v), v.Type())
	if err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode value: %w", err)
	}
	return out, nil
}

// elem is s[i], or a null of the other side's element type past the end.
func elem(s []cty.Value, i int, other cty.Value) cty.Value {
	if i < len(s) {
		return s[i]
	}
	if ty := other.Type(); ty.IsListType() {
		return cty.NullVal(ty.ElementType())
	}
	if ty := other.Type(); ty.IsTupleType() && i < len(ty.TupleElementTypes()) {
		return cty.NullVal(ty.TupleElementTypes()[i])
	}
	return cty.NullVal(cty.DynamicPseudoType)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return strings.Join([]string{path, name}, ".")
}
