package tofu

import (
	"fmt"

	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/provider-client/tofuprovider/providerschema"
)

// Block is a resource type's schema, or one of its nested blocks: its
// attributes and the blocks nested in it, by name.
type Block struct {
	Description string
	Attributes  map[string]Attribute
	Blocks      map[string]NestedBlock
}

// Attribute is an attribute of a [Block].
type Attribute struct {
	Usage Usage
	// Type is the attribute's type, when Nested is nil.
	Type cty.Type
	// Nested holds the attributes of an attribute whose value is an object,
	// or a collection of objects, described attribute by attribute.
	Nested      *NestedBlock
	Sensitive   bool
	WriteOnly   bool
	Deprecated  bool
	Description string
}

// NestedBlock is a block nested in a [Block], or the object type of a
// nested [Attribute], with how it repeats. MinItems and MaxItems are 0 when
// unbounded, and always 0 for a nested attribute.
type NestedBlock struct {
	Nesting  Nesting
	MinItems int64
	MaxItems int64
	Block
}

// Usage says who sets an attribute: the configuration, the provider, or
// either.
type Usage int

const (
	// UsageRequired is set by the configuration, always.
	UsageRequired Usage = iota
	// UsageOptional is set by the configuration, or left null.
	UsageOptional
	// UsageOptionalComputed is set by the configuration, or else by the
	// provider.
	UsageOptionalComputed
	// UsageComputed is set by the provider only.
	UsageComputed
)

// Nesting says how a [NestedBlock] repeats in its parent.
type Nesting int

const (
	// NestingSingle is one object, or null.
	NestingSingle Nesting = iota
	// NestingGroup is one object, never null.
	NestingGroup
	// NestingList is a list of objects.
	NestingList
	// NestingSet is a set of objects.
	NestingSet
	// NestingMap is a map of objects, by string key.
	NestingMap
)

// blockFrom copies a provider-client schema block into a [Block].
func blockFrom(bt providerschema.BlockType) (Block, error) {
	b := Block{Attributes: map[string]Attribute{}, Blocks: map[string]NestedBlock{}}
	for name, a := range bt.Attributes() {
		attr, err := attributeFrom(a)
		if err != nil {
			return Block{}, fmt.Errorf("attribute %q: %w", name, err)
		}
		b.Attributes[name] = attr
	}
	for name, nbt := range bt.NestedBlockTypes() {
		inner, err := blockFrom(nbt)
		if err != nil {
			return Block{}, fmt.Errorf("block %q: %w", name, err)
		}
		nesting, err := nestingFrom(nbt.Nesting())
		if err != nil {
			return Block{}, fmt.Errorf("block %q: %w", name, err)
		}
		lo, hi := nbt.ItemLimits()
		b.Blocks[name] = NestedBlock{Nesting: nesting, MinItems: lo, MaxItems: hi, Block: inner}
	}
	return b, nil
}

func attributeFrom(a providerschema.Attribute) (Attribute, error) {
	attr := Attribute{
		Sensitive:  a.IsSensitive(),
		WriteOnly:  a.IsWriteOnly(),
		Deprecated: a.IsDeprecated(),
	}
	attr.Description, _ = a.DocDescription() // the second value is the text's format
	switch a.Usage() {
	case providerschema.AttributeRequired:
		attr.Usage = UsageRequired
	case providerschema.AttributeOptional:
		attr.Usage = UsageOptional
	case providerschema.AttributeOptionalComputed:
		attr.Usage = UsageOptionalComputed
	case providerschema.AttributeComputed:
		attr.Usage = UsageComputed
	default:
		return Attribute{}, fmt.Errorf("unsupported usage %v", a.Usage())
	}

	if nt := a.NestedType(); nt != nil {
		nesting, err := nestingFrom(nt.Nesting())
		if err != nil {
			return Attribute{}, err
		}
		nb := NestedBlock{Nesting: nesting, Block: Block{Attributes: map[string]Attribute{}, Blocks: map[string]NestedBlock{}}}
		for name, na := range nt.Attributes() {
			inner, err := attributeFrom(na)
			if err != nil {
				return Attribute{}, fmt.Errorf("attribute %q: %w", name, err)
			}
			nb.Attributes[name] = inner
		}
		attr.Nested = &nb
		return attr, nil
	}
	ty, err := a.Type().AsCtyType()
	if err != nil {
		return Attribute{}, err
	}
	attr.Type = ty
	return attr, nil
}

func nestingFrom(n providerschema.NestingMode) (Nesting, error) {
	switch n {
	case providerschema.NestingSingle:
		return NestingSingle, nil
	case providerschema.NestingGroup:
		return NestingGroup, nil
	case providerschema.NestingList:
		return NestingList, nil
	case providerschema.NestingSet:
		return NestingSet, nil
	case providerschema.NestingMap:
		return NestingMap, nil
	}
	return 0, fmt.Errorf("unsupported nesting mode %s", n)
}
