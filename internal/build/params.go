package build

// Module parameters declared in Go:
//
//	//gokm:param rd_size Size of each RAM disk in kbytes.
//	var rdSize = 16 << 10
//
// becomes a module parameter "rd_size" (insmod gobrd.ko rd_size=65536,
// /sys/module/<name>/parameters/rd_size, modinfo). The variable's
// initializer is the default. The value given at load time is stored in it
// by a generated init() that runs before the project's own init functions
// (its file name sorts first); package-level variable initializers that
// read a parameter still see the default.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// moduleParam is a //gokm:param variable.
type moduleParam struct {
	Name   string // parameter name
	GoName string
	GoType string
	Desc   string
	Value  constant.Value // default; nil means the zero value
	Pos    token.Position
}

// paramTypes maps Go types to kernel param types and C storage types.
var paramTypes = map[string]struct{ kind, ctype string }{
	"int":    {"long", "long"},
	"int64":  {"long", "long"},
	"int32":  {"int", "int"},
	"int16":  {"short", "short"},
	"uint":   {"ulong", "unsigned long"},
	"uint64": {"ullong", "unsigned long long"},
	"uint32": {"uint", "unsigned int"},
	"uint16": {"ushort", "unsigned short"},
	"byte":   {"byte", "unsigned char"},
	"uint8":  {"byte", "unsigned char"},
	"bool":   {"bool", "bool"},
	"string": {"charp", "char *"},
}

var paramNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// scanParams collects //gokm:param variables of a file.
func scanParams(f *ast.File, fset *token.FileSet, path string) ([]moduleParam, error) {
	var params []moduleParam
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			doc := vs.Doc
			if doc == nil && len(gd.Specs) == 1 {
				doc = gd.Doc
			}
			if doc == nil {
				continue
			}
			for _, c := range doc.List {
				args, ok := strings.CutPrefix(c.Text, "//gokm:param")
				if !ok {
					continue
				}
				pos := fset.Position(c.Pos())
				fields := strings.Fields(args)
				if len(fields) == 0 || !paramNameRE.MatchString(fields[0]) {
					return nil, fmt.Errorf("%s: //gokm:param needs a parameter name (letters, digits, _)", pos)
				}
				if f.Name.Name != "main" {
					return nil, fmt.Errorf("%s: //gokm:param variables must be in package main", pos)
				}
				if len(vs.Names) != 1 {
					return nil, fmt.Errorf("%s: //gokm:param declares one variable at a time", pos)
				}
				p := moduleParam{
					Name:   fields[0],
					GoName: vs.Names[0].Name,
					Desc:   strings.Join(fields[1:], " "),
					Pos:    pos,
				}
				var val ast.Expr
				if len(vs.Values) == 1 {
					val = vs.Values[0]
				}
				if vs.Type != nil {
					id, ok := vs.Type.(*ast.Ident)
					if !ok {
						return nil, fmt.Errorf("%s: //gokm:param: unsupported type", pos)
					}
					p.GoType = id.Name
				}
				if val != nil {
					v, err := evalConst(val)
					if err != nil {
						return nil, fmt.Errorf("%s: //gokm:param default: %v", pos, err)
					}
					p.Value = v
					if p.GoType == "" {
						switch v.Kind() {
						case constant.Int:
							p.GoType = "int"
						case constant.Bool:
							p.GoType = "bool"
						case constant.String:
							p.GoType = "string"
						default:
							return nil, fmt.Errorf("%s: //gokm:param: unsupported default", pos)
						}
					}
				}
				if _, ok := paramTypes[p.GoType]; !ok {
					return nil, fmt.Errorf("%s: //gokm:param: type %q not supported (integers, bool, string)", pos, p.GoType)
				}
				params = append(params, p)
			}
		}
	}
	return params, nil
}

// evalConst evaluates a constant expression made of literals.
func evalConst(e ast.Expr) (constant.Value, error) {
	switch e := e.(type) {
	case *ast.BasicLit:
		v := constant.MakeFromLiteral(e.Value, e.Kind, 0)
		if v.Kind() == constant.Unknown {
			return nil, fmt.Errorf("bad literal %s", e.Value)
		}
		return v, nil
	case *ast.Ident:
		switch e.Name {
		case "true":
			return constant.MakeBool(true), nil
		case "false":
			return constant.MakeBool(false), nil
		}
	case *ast.ParenExpr:
		return evalConst(e.X)
	case *ast.UnaryExpr:
		x, err := evalConst(e.X)
		if err != nil {
			return nil, err
		}
		return constant.UnaryOp(e.Op, x, 0), nil
	case *ast.BinaryExpr:
		x, err := evalConst(e.X)
		if err != nil {
			return nil, err
		}
		y, err := evalConst(e.Y)
		if err != nil {
			return nil, err
		}
		switch e.Op {
		case token.SHL, token.SHR:
			s, ok := constant.Uint64Val(y)
			if !ok {
				return nil, fmt.Errorf("bad shift")
			}
			return constant.Shift(x, e.Op, uint(s)), nil
		case token.QUO:
			if x.Kind() == constant.Int && y.Kind() == constant.Int {
				return constant.BinaryOp(x, token.QUO_ASSIGN, y), nil // integer division
			}
		}
		return constant.BinaryOp(x, e.Op, y), nil
	}
	return nil, fmt.Errorf("must be a constant expression of literals")
}

// writeParamsGo adds the Go side to the copied source: the C storage of
// each parameter as an extern, copied into the variable at init.
func writeParamsGo(dir string, params []moduleParam) error {
	if len(params) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("// Code generated by gokm for //gokm:param. DO NOT EDIT.\n\npackage main\n\nimport \"unsafe\"\n\n")
	for _, p := range params {
		if p.GoType == "string" {
			fmt.Fprintf(&b, "//go:extern gokm_param_%s\nvar gokmParam_%s *byte\n\n", p.Name, p.Name)
		} else {
			fmt.Fprintf(&b, "//go:extern gokm_param_%s\nvar gokmParam_%s %s\n\n", p.Name, p.Name, p.GoType)
		}
	}
	b.WriteString("func init() {\n")
	for _, p := range params {
		if p.GoType == "string" {
			fmt.Fprintf(&b, "\t%s = gokmParamString(gokmParam_%s)\n", p.GoName, p.Name)
		} else {
			fmt.Fprintf(&b, "\t%s = gokmParam_%s\n", p.GoName, p.Name)
		}
	}
	b.WriteString("}\n\n")
	b.WriteString(`func gokmParamString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}
`)
	return os.WriteFile(filepath.Join(dir, "aaa_gokm_params.go"), []byte(b.String()), 0o644)
}

// paramsC is the C side: storage with the default, module_param_named and
// the description.
func paramsC(params []moduleParam) ([]byte, error) {
	var b strings.Builder
	b.WriteString("// SPDX-License-Identifier: GPL-2.0\n/* Generated by gokm for //gokm:param. */\n#include <linux/module.h>\n#include <linux/moduleparam.h>\n\n")
	for _, p := range params {
		t := paramTypes[p.GoType]
		def := "0"
		switch {
		case p.GoType == "string":
			def = "NULL"
			if p.Value != nil {
				def = cString(constant.StringVal(p.Value))
			}
		case p.GoType == "bool":
			def = "false"
			if p.Value != nil && p.Value.Kind() == constant.Bool && constant.BoolVal(p.Value) {
				def = "true"
			} else if p.Value != nil && p.Value.Kind() != constant.Bool {
				return nil, fmt.Errorf("%s: //gokm:param default is not a bool", p.Pos)
			}
		case p.Value != nil:
			v := constant.ToInt(p.Value)
			if v.Kind() != constant.Int {
				return nil, fmt.Errorf("%s: //gokm:param default is not an integer", p.Pos)
			}
			def = v.ExactString()
			if strings.HasPrefix(t.kind, "u") || t.kind == "byte" {
				if constant.Sign(v) < 0 {
					return nil, fmt.Errorf("%s: //gokm:param default is negative for %s", p.Pos, p.GoType)
				}
				def += "ULL"
			} else {
				def += "LL"
			}
		}
		fmt.Fprintf(&b, "%s gokm_param_%s = %s;\n", t.ctype, p.Name, def)
		fmt.Fprintf(&b, "module_param_named(%s, gokm_param_%s, %s, 0444);\n", p.Name, p.Name, t.kind)
		if p.Desc != "" {
			fmt.Fprintf(&b, "MODULE_PARM_DESC(%s, %s);\n", p.Name, cString(p.Desc))
		}
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}
