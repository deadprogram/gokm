// Package btf reads BPF Type Format data: the kernel's own description of
// its types and functions (the .BTF section of vmlinux, or
// /sys/kernel/btf/vmlinux). gokm uses it to generate Go declarations with
// the exact struct layouts and function signatures of the target kernel.
package btf

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// Kind is a BTF type kind.
type Kind uint8

const (
	KindVoid Kind = iota
	KindInt
	KindPtr
	KindArray
	KindStruct
	KindUnion
	KindEnum
	KindFwd
	KindTypedef
	KindVolatile
	KindConst
	KindRestrict
	KindFunc
	KindFuncProto
	KindVar
	KindDatasec
	KindFloat
	KindDeclTag
	KindTypeTag
	KindEnum64
)

// Int encoding bits.
const (
	IntSigned = 1 << 0
	IntChar   = 1 << 1
	IntBool   = 1 << 2
)

// Type is one BTF type. Which fields are set depends on Kind.
type Type struct {
	ID       uint32
	Kind     Kind
	Name     string
	KindFlag bool
	Size     uint32 // Int, Struct, Union, Enum, Enum64, Float, Datasec
	Ref      uint32 // Ptr, Typedef, Volatile, Const, Restrict, Func, FuncProto (return), Var, tags

	IntEncoding uint8
	IntBits     uint8

	ArrayElem  uint32
	ArrayIndex uint32
	ArrayLen   uint32

	Members []Member // Struct, Union
	Enums   []Enum   // Enum, Enum64
	Params  []Param  // FuncProto
}

// Member is a struct/union member. Offsets are in bits.
type Member struct {
	Name         string
	Type         uint32
	BitOffset    uint32
	BitfieldSize uint32 // 0 if not a bitfield
}

// Enum is an enumerator.
type Enum struct {
	Name  string
	Value int64
}

// Param is a function parameter.
type Param struct {
	Name string
	Type uint32
}

// Spec is a parsed BTF blob. Types[0] is void.
type Spec struct {
	Types []*Type
}

// Load reads BTF from an ELF file's .BTF section (e.g. vmlinux) or a raw
// BTF file (e.g. /sys/kernel/btf/vmlinux).
func Load(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte("\x7fELF")) {
		f, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		sec := f.Section(".BTF")
		if sec == nil {
			return nil, fmt.Errorf("%s: no .BTF section", path)
		}
		if data, err = sec.Data(); err != nil {
			return nil, err
		}
	}
	return Parse(data)
}

// Parse parses a raw BTF blob.
func Parse(data []byte) (*Spec, error) {
	if len(data) < 24 {
		return nil, errors.New("btf: too short")
	}
	le := binary.LittleEndian
	if le.Uint16(data) != 0xeb9f {
		return nil, errors.New("btf: bad magic (not little-endian BTF?)")
	}
	hdrLen := le.Uint32(data[4:])
	typeOff, typeLen := le.Uint32(data[8:]), le.Uint32(data[12:])
	strOff, strLen := le.Uint32(data[16:]), le.Uint32(data[20:])
	body := data[hdrLen:]
	if uint64(typeOff)+uint64(typeLen) > uint64(len(body)) || uint64(strOff)+uint64(strLen) > uint64(len(body)) {
		return nil, errors.New("btf: sections out of bounds")
	}
	types := body[typeOff : typeOff+typeLen]
	strs := body[strOff : strOff+strLen]
	str := func(off uint32) string {
		if int(off) >= len(strs) {
			return ""
		}
		end := bytes.IndexByte(strs[off:], 0)
		if end < 0 {
			return string(strs[off:])
		}
		return string(strs[off : int(off)+end])
	}

	s := &Spec{Types: []*Type{{Kind: KindVoid}}}
	for p := 0; p < len(types); {
		if p+12 > len(types) {
			return nil, errors.New("btf: truncated type")
		}
		info := le.Uint32(types[p+4:])
		t := &Type{
			ID:       uint32(len(s.Types)),
			Name:     str(le.Uint32(types[p:])),
			Kind:     Kind((info >> 24) & 0x1f),
			KindFlag: info>>31 == 1,
		}
		vlen := int(info & 0xffff)
		sizeOrType := le.Uint32(types[p+8:])
		p += 12
		need := func(n int) error {
			if p+n > len(types) {
				return errors.New("btf: truncated type data")
			}
			return nil
		}
		switch t.Kind {
		case KindInt:
			if err := need(4); err != nil {
				return nil, err
			}
			v := le.Uint32(types[p:])
			t.Size = sizeOrType
			t.IntEncoding = uint8(v >> 24 & 0xf)
			t.IntBits = uint8(v)
			p += 4
		case KindPtr, KindTypedef, KindVolatile, KindConst, KindRestrict, KindFunc, KindTypeTag:
			t.Ref = sizeOrType
		case KindArray:
			if err := need(12); err != nil {
				return nil, err
			}
			t.ArrayElem, t.ArrayIndex, t.ArrayLen = le.Uint32(types[p:]), le.Uint32(types[p+4:]), le.Uint32(types[p+8:])
			p += 12
		case KindStruct, KindUnion:
			if err := need(12 * vlen); err != nil {
				return nil, err
			}
			t.Size = sizeOrType
			for i := 0; i < vlen; i++ {
				m := Member{Name: str(le.Uint32(types[p:])), Type: le.Uint32(types[p+4:])}
				off := le.Uint32(types[p+8:])
				if t.KindFlag {
					m.BitfieldSize, m.BitOffset = off>>24, off&0xffffff
				} else {
					m.BitOffset = off
				}
				t.Members = append(t.Members, m)
				p += 12
			}
		case KindEnum:
			if err := need(8 * vlen); err != nil {
				return nil, err
			}
			t.Size = sizeOrType
			for i := 0; i < vlen; i++ {
				v := int64(int32(le.Uint32(types[p+4:])))
				if !t.KindFlag {
					v = int64(le.Uint32(types[p+4:])) // unsigned enum
				}
				t.Enums = append(t.Enums, Enum{Name: str(le.Uint32(types[p:])), Value: v})
				p += 8
			}
		case KindEnum64:
			if err := need(12 * vlen); err != nil {
				return nil, err
			}
			t.Size = sizeOrType
			for i := 0; i < vlen; i++ {
				v := int64(uint64(le.Uint32(types[p+8:]))<<32 | uint64(le.Uint32(types[p+4:])))
				t.Enums = append(t.Enums, Enum{Name: str(le.Uint32(types[p:])), Value: v})
				p += 12
			}
		case KindFwd:
		case KindFuncProto:
			if err := need(8 * vlen); err != nil {
				return nil, err
			}
			t.Ref = sizeOrType
			for i := 0; i < vlen; i++ {
				t.Params = append(t.Params, Param{Name: str(le.Uint32(types[p:])), Type: le.Uint32(types[p+4:])})
				p += 8
			}
		case KindVar:
			t.Ref = sizeOrType
			p += 4
		case KindDatasec:
			t.Size = sizeOrType
			p += 12 * vlen
		case KindFloat:
			t.Size = sizeOrType
		case KindDeclTag:
			t.Ref = sizeOrType
			p += 4
		default:
			return nil, fmt.Errorf("btf: unknown kind %d", t.Kind)
		}
		s.Types = append(s.Types, t)
	}
	return s, nil
}

// Type returns the type with the given ID (nil if out of range).
func (s *Spec) Type(id uint32) *Type {
	if int(id) >= len(s.Types) {
		return nil
	}
	return s.Types[id]
}

// Skip follows typedefs, qualifiers and type tags to the underlying type.
func (s *Spec) Skip(id uint32) *Type {
	for i := 0; i < 64; i++ {
		t := s.Type(id)
		if t == nil {
			return s.Types[0]
		}
		switch t.Kind {
		case KindTypedef, KindVolatile, KindConst, KindRestrict, KindTypeTag:
			id = t.Ref
		default:
			return t
		}
	}
	return s.Types[0]
}

// SkipQualifiers follows only qualifiers and type tags (not typedefs).
func (s *Spec) SkipQualifiers(id uint32) *Type {
	for i := 0; i < 64; i++ {
		t := s.Type(id)
		if t == nil {
			return s.Types[0]
		}
		switch t.Kind {
		case KindVolatile, KindConst, KindRestrict, KindTypeTag:
			id = t.Ref
		default:
			return t
		}
	}
	return s.Types[0]
}

// SizeOf returns the size in bytes of a type (0 for void/functions).
func (s *Spec) SizeOf(id uint32) uint32 {
	t := s.Skip(id)
	switch t.Kind {
	case KindInt, KindStruct, KindUnion, KindEnum, KindEnum64, KindFloat:
		return t.Size
	case KindPtr:
		return 8
	case KindArray:
		return t.ArrayLen * s.SizeOf(t.ArrayElem)
	}
	return 0
}
