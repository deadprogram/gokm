package build

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Stack-usage analysis of the Go object. Go code runs on 16 KiB kernel
// stacks (the caller's, or a goroutine kthread's), shared with the kernel
// frames around it. gokm compiles an analysis copy of the object with
// LLVM's per-function stack sizes (-fstack-size-section) and one section
// per function (so calls are relocations), builds the call graph, and
// reports the deepest path from every entry point: the functions C calls
// (//export), and goroutine start functions.
//
// Indirect calls (interfaces, closures) and calls into the kernel can't be
// followed, so results are lower bounds when they're involved ("≥").
// Recursion makes the depth unbounded.

type stackEntry struct {
	Name       string
	Bytes      uint64
	Path       []string
	Indirect   bool   // the path has indirect calls: a lower bound
	Recursive  bool   // reaches recursion in the project's code
	RecursesIn string // a function of the project that recurses
}

type stackGraph struct {
	frame     map[string]uint64
	calls     map[string][]string
	indirect  map[string]bool
	exported  []string // global functions
	goroutine []string // goroutine entry wrappers

	memo     map[string]*stackEntry
	visiting map[string]bool
	modPath  string
}

// own reports whether a function belongs to the project or to gokm (not
// the Go runtime/standard library, whose internal recursion isn't the
// project's concern).
func (g *stackGraph) own(fn string) bool {
	s := strings.TrimLeft(fn, "(*")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		if j := strings.Index(s[i:], "."); j >= 0 {
			s = s[:i+j]
		}
	} else if j := strings.Index(s, "."); j >= 0 {
		s = s[:j]
	}
	return s == "main" || s == g.modPath || strings.HasPrefix(s, g.modPath+"/") || strings.HasPrefix(s, "gokm/") ||
		!strings.Contains(fn, ".")
}

var (
	irDefineRe = regexp.MustCompile(`^define [^@]*@("([^"]+)"|([^\s(]+))\(`)
	irCallRe   = regexp.MustCompile(`\bcall [^@%(]*([@%])("([^"]+)"|([^\s(]+))\(`)
)

// irCallGraph adds the direct calls (and which functions make indirect
// calls) found in LLVM IR to g.
func irCallGraph(ir []byte, g *stackGraph) {
	cur := ""
	for _, line := range strings.Split(string(ir), "\n") {
		if m := irDefineRe.FindStringSubmatch(line); m != nil {
			cur = m[2] + m[3]
			continue
		}
		if cur == "" {
			continue
		}
		if line == "}" {
			cur = ""
			continue
		}
		for _, m := range irCallRe.FindAllStringSubmatch(line, -1) {
			if m[1] == "%" {
				g.indirect[cur] = true
				continue
			}
			callee := m[3] + m[4]
			if !strings.HasPrefix(callee, "llvm.") {
				g.calls[cur] = append(g.calls[cur], callee)
			}
		}
	}
}

func analyzeStack(obj, ll, modPath string) (*stackGraph, error) {
	f, err := elf.Open(obj)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	syms, err := f.Symbols()
	if err != nil {
		return nil, err
	}
	g := &stackGraph{frame: map[string]uint64{}, calls: map[string][]string{}, indirect: map[string]bool{},
		memo: map[string]*stackEntry{}, visiting: map[string]bool{}, modPath: modPath}

	// Functions by section (one per section with -ffunction-sections).
	type fn struct {
		name       string
		start, end uint64
	}
	bySection := map[elf.SectionIndex][]fn{}
	for _, s := range syms {
		if elf.ST_TYPE(s.Info) != elf.STT_FUNC || s.Section == elf.SHN_UNDEF || s.Section >= elf.SHN_LORESERVE {
			continue
		}
		bySection[s.Section] = append(bySection[s.Section], fn{s.Name, s.Value, s.Value + s.Size})
		if elf.ST_BIND(s.Info) == elf.STB_GLOBAL && !strings.Contains(s.Name, ".") && !localized(s.Name) {
			g.exported = append(g.exported, s.Name)
		}
		if strings.HasSuffix(s.Name, "$gowrapper") {
			g.goroutine = append(g.goroutine, s.Name)
		}
	}
	funcAt := func(sec elf.SectionIndex, off uint64) string {
		for _, f := range bySection[sec] {
			if off >= f.start && off < f.end {
				return f.name
			}
		}
		if fs := bySection[sec]; len(fs) == 1 {
			return fs[0].name
		}
		return ""
	}
	// symName resolves a relocation symbol (function or section+addend).
	symName := func(idx uint32, addend int64) string {
		if idx == 0 || int(idx) > len(syms) {
			return ""
		}
		s := syms[idx-1]
		if elf.ST_TYPE(s.Info) == elf.STT_SECTION {
			return funcAt(s.Section, uint64(addend))
		}
		return s.Name
	}

	// Frame sizes: .stack_sizes entries (address relocation + ULEB128 size).
	for _, sec := range f.Sections {
		if sec.Type != elf.SHT_RELA || f.Sections[sec.Info].Name != ".stack_sizes" {
			continue
		}
		data, err := sec.Data()
		if err != nil {
			return nil, err
		}
		sd, err := f.Sections[sec.Info].Data()
		if err != nil {
			return nil, err
		}
		for p := 0; p+24 <= len(data); p += 24 {
			off := binary.LittleEndian.Uint64(data[p:])
			info := binary.LittleEndian.Uint64(data[p+8:])
			addend := int64(binary.LittleEndian.Uint64(data[p+16:]))
			name := symName(uint32(info>>32), addend)
			if name == "" || int(off)+8 >= len(sd) {
				continue
			}
			size, _ := binary.Uvarint(sd[off+8:])
			g.frame[name] = size
		}
	}

	// Calls: from the optimized IR (calls within one section, like
	// recursion, have no relocation in the object).
	ir, err := os.ReadFile(ll)
	if err != nil {
		return nil, err
	}
	irCallGraph(ir, g)
	sort.Strings(g.exported)
	sort.Strings(g.goroutine)
	return g, nil
}

func localized(name string) bool {
	for _, pat := range localizedSymbols {
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// deepest computes the deepest path from fn.
func (g *stackGraph) deepest(fn string) *stackEntry {
	if e, ok := g.memo[fn]; ok {
		return e
	}
	if g.visiting[fn] {
		e := &stackEntry{Name: fn, Recursive: g.own(fn), Path: []string{fn}}
		if e.Recursive {
			e.RecursesIn = fn
		}
		return e
	}
	g.visiting[fn] = true
	e := &stackEntry{Name: fn, Bytes: g.frame[fn] + 8, Path: []string{fn}, Indirect: g.indirect[fn]}
	var best *stackEntry
	seen := map[string]bool{}
	for _, c := range g.calls[fn] {
		if c == fn {
			if g.own(fn) {
				e.Recursive, e.RecursesIn = true, fn
			}
			continue
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		ce := g.deepest(c)
		if ce.Recursive && !e.Recursive {
			e.Recursive, e.RecursesIn = true, ce.RecursesIn
		}
		e.Indirect = e.Indirect || ce.Indirect
		if best == nil || ce.Bytes > best.Bytes {
			best = ce
		}
	}
	if best != nil {
		e.Bytes += best.Bytes
		e.Path = append(e.Path, best.Path...)
	}
	delete(g.visiting, fn)
	g.memo[fn] = e
	return e
}

// entries returns the deepest path from each entry point, deepest first.
func (g *stackGraph) entries() []*stackEntry {
	var out []*stackEntry
	for _, fn := range append(append([]string{}, g.exported...), g.goroutine...) {
		out = append(out, g.deepest(fn))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

func (e *stackEntry) describe() string {
	bound := ""
	if e.Indirect {
		bound = "≥"
	}
	path := e.Path
	if len(path) > 6 {
		path = append(append(append([]string{}, path[:3]...), "…"), path[len(path)-2:]...)
	}
	s := fmt.Sprintf("%s: %s%d bytes (%s)", e.Name, bound, e.Bytes, strings.Join(path, " → "))
	if e.Recursive {
		s += fmt.Sprintf("; reaches recursion in %s: unbounded", e.RecursesIn)
	}
	return s
}
