package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleParams(t *testing.T) {
	proj, dst := t.TempDir(), filepath.Join(t.TempDir(), "src")
	src := `package main

// Number of disks.
//
//gokm:param rd_nr Maximum number of devices
var rdNr = 16

//gokm:param rd_size Size in KiB.
var rdSize uint = 4 << 10

var (
	//gokm:param verbose Log more
	verbose bool

	//gokm:param label Volume label
	label = "go" + "km"

	//gokm:param skew
	skew int32 = -(3 * 2)
)

func main() {}
`
	os.WriteFile(filepath.Join(proj, "go.mod"), []byte("module m\n"), 0o644)
	os.WriteFile(filepath.Join(proj, "main.go"), []byte(src), 0o644)
	scan, err := prepareSource(proj, dst)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range scan.params {
		got = append(got, p.Name+":"+p.GoName+":"+p.GoType+":"+p.Desc)
	}
	want := "rd_nr:rdNr:int:Maximum number of devices rd_size:rdSize:uint:Size in KiB. " +
		"verbose:verbose:bool:Log more label:label:string:Volume label skew:skew:int32:"
	if strings.Join(got, " ") != want {
		t.Errorf("params:\n got %s\nwant %s", strings.Join(got, " "), want)
	}

	c, err := paramsC(scan.params)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"long gokm_param_rd_nr = 16LL;\nmodule_param_named(rd_nr, gokm_param_rd_nr, long, 0444);\n" +
			`MODULE_PARM_DESC(rd_nr, "Maximum number of devices");`,
		"unsigned long gokm_param_rd_size = 4096ULL;\nmodule_param_named(rd_size, gokm_param_rd_size, ulong, 0444);",
		"bool gokm_param_verbose = false;\nmodule_param_named(verbose, gokm_param_verbose, bool, 0444);",
		`char * gokm_param_label = "gokm";` + "\nmodule_param_named(label, gokm_param_label, charp, 0444);",
		"int gokm_param_skew = -6LL;\nmodule_param_named(skew, gokm_param_skew, int, 0444);\n\n",
	} {
		if !strings.Contains(string(c), w) {
			t.Errorf("C missing %q\n--- got:\n%s", w, c)
		}
	}

	g, err := os.ReadFile(filepath.Join(dst, "aaa_gokm_params.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"//go:extern gokm_param_rd_nr\nvar gokmParam_rd_nr int\n",
		"//go:extern gokm_param_label\nvar gokmParam_label *byte\n",
		"\trdSize = gokmParam_rd_size\n",
		"\tlabel = gokmParamString(gokmParam_label)\n",
	} {
		if !strings.Contains(string(g), w) {
			t.Errorf("Go missing %q\n--- got:\n%s", w, g)
		}
	}
}

func TestModuleParamErrors(t *testing.T) {
	for name, src := range map[string]string{
		"no name":     "package main\n//gokm:param\nvar x = 1\n",
		"bad type":    "package main\n//gokm:param x\nvar x float64\n",
		"not const":   "package main\nconst c = 1\n//gokm:param x\nvar x = c\n",
		"duplicate":   "package main\n//gokm:param x\nvar a = 1\n//gokm:param x\nvar b = 2\n",
		"negative u":  "package main\n//gokm:param x\nvar x uint = -1\n",
		"not main":    "package lib\n//gokm:param x\nvar x = 1\n",
		"two at once": "package main\n//gokm:param x\nvar a, b = 1, 2\n",
	} {
		proj, dst := t.TempDir(), filepath.Join(t.TempDir(), "src")
		os.WriteFile(filepath.Join(proj, "go.mod"), []byte("module m\n"), 0o644)
		os.WriteFile(filepath.Join(proj, "main.go"), []byte(src), 0o644)
		scan, err := prepareSource(proj, dst)
		if err == nil {
			_, err = paramsC(scan.params)
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
