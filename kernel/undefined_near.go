package kernel

// undefined_near.go — what the build defined under the name a link could not find.
//
// 11/25 04:34:25 the link of e2e_all_check failed on "undefined reference to `e2e_embed_main()'".
// build.sh compiled embed_parity.cu with -Dmain=e2e_embed_main, and its main takes (int argc,
// char** argv): the object defined e2e_embed_main(int, char**), a symbol of another name to the
// linker, and nothing the conversation was given said so.

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var undefinedRefRe = regexp.MustCompile("undefined reference to [`']([^'`]+)'")

const (
	undefinedNearObjMax = 4000 // object files read at most
	undefinedNearMax    = 20   // lines given at most
	// %[1]s the lines
	undefinedNearFmt = "[WHAT THE BUILD DEFINED UNDER THE NAMES THE LINK DID NOT FIND — measured with nm -C on the object files build.sh wrote]\n%[1]s"
	// %[1]s the reference not found, %[2]s a definition of that name, %[3]s the object
	undefinedNearLineFmt = "  undefined %[1]s — %[2]s is defined in %[3]s\n"
	// %[1]s the reference not found
	undefinedNoneLineFmt = "  undefined %[1]s — no object file of the workspace defines a function of that name\n"
)

// funcBaseName: a demangled function's name without its parameters.
func funcBaseName(sym string) string {
	if i := strings.Index(sym, "("); i >= 0 {
		sym = sym[:i]
	}
	return strings.TrimSpace(sym)
}

// undefinedRefNear: for each function the link output says it found no definition of, the
// definitions of a function of the same name the workspace's object files hold, with the object;
// "" when nm is not here, no reference is undefined, or no object file was written.
func undefinedRefNear(ws, out string) string {
	want := map[string][]string{} // base name -> the references not found
	var names []string
	for _, m := range undefinedRefRe.FindAllStringSubmatch(out, -1) {
		b := funcBaseName(m[1])
		if b == "" {
			continue
		}
		if _, ok := want[b]; !ok {
			names = append(names, b)
		}
		if !slices.Contains(want[b], m[1]) {
			want[b] = append(want[b], m[1])
		}
	}
	if len(names) == 0 {
		return ""
	}
	defs := objectFuncDefs(ws, func(b string) bool { return want[b] != nil })
	if defs == nil {
		return ""
	}
	var lb strings.Builder
	n := 0
	for _, b := range names {
		for _, ref := range want[b] {
			if n == undefinedNearMax {
				break
			}
			if len(defs[b]) == 0 {
				lb.WriteString(fmt.Sprintf(undefinedNoneLineFmt, ref))
				n++
				continue
			}
			for _, d := range defs[b] {
				if n == undefinedNearMax {
					break
				}
				sym, rest, _ := strings.Cut(d, "\x00")
				obj, _, _ := strings.Cut(rest, "\x00")
				lb.WriteString(fmt.Sprintf(undefinedNearLineFmt, ref, sym, obj))
				n++
			}
		}
	}
	return fmt.Sprintf(undefinedNearFmt, lb.String())
}

var multipleDefRe = regexp.MustCompile("multiple definition of [`']([^'`]+)'")

const (
	// %[1]s the lines
	multipleDefFmt = "[WHERE THE FUNCTIONS THE LINK FOUND DEFINED TWICE ARE WRITTEN — measured from the workspace's sources]\n%[1]s"
	// %[1]s the function, %[2]s the file defining it, %[3]s the files including that file
	multipleDefLineFmt = "  %[1]s — written in %[2]s, which %[3]s #include\n"
	// %[1]s the function, %[2]s the file defining it
	multipleDefNoIncLineFmt = "  %[1]s — written in %[2]s, which no source of the workspace #includes\n"
	// %[1]s the function
	multipleDefNoneLineFmt = "  %[1]s — no source of the workspace has a __global__ definition of it\n"
)

// linkFuncName: a demangled symbol's function name — no parameters, no namespace, no CUDA
// launch stub prefix ("ix_se::__device_stub__swiglu_kernel(float const*, …)" → swiglu_kernel).
func linkFuncName(sym string) string {
	n := funcBaseName(sym)
	if i := strings.LastIndex(n, "::"); i >= 0 {
		n = n[i+2:]
	}
	return strings.TrimPrefix(n, "__device_stub__")
}

// multipleDefNear: for each kernel a failed link found defined twice, the workspace files
// defining it and the sources that #include each of them. 11/25 08:08:14 swiglu_kernel,
// scalar_gate_kernel and scale_kernel of src/ix_shared_expert.cu were defined in both
// pic_e2e_moe.o and pic_e2e_se.o: tests/k39_integration/moe_parity.cu and tests/test_shared_expert.cu
// both #include that .cu file, and build.sh linked both objects into one program.
func multipleDefNear(ws, out string) string {
	var names []string
	seen := map[string]bool{}
	for _, m := range multipleDefRe.FindAllStringSubmatch(out, -1) {
		n := linkFuncName(m[1])
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	incRe := regexp.MustCompile(`(?m)^\s*#\s*include\s*"([^"]+)"`)
	includes := map[string][]string{} // base name of an included file -> the sources including it
	_ = filepath.WalkDir(ws, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if e.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !kernelSourceExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		if st, err := e.Info(); err != nil || st.Size() > 4<<20 {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		for _, m := range incRe.FindAllStringSubmatch(string(b), -1) {
			includes[filepath.Base(m[1])] = append(includes[filepath.Base(m[1])], filepath.ToSlash(rel))
		}
		return nil
	})
	var b strings.Builder
	for i, n := range names {
		if i == undefinedNearMax {
			break
		}
		defs := kernelDefiningFiles(ws, n)
		if len(defs) == 0 {
			b.WriteString(fmt.Sprintf(multipleDefNoneLineFmt, n))
			continue
		}
		for _, d := range defs {
			inc := append([]string(nil), includes[filepath.Base(d)]...)
			sort.Strings(inc)
			if len(inc) == 0 {
				b.WriteString(fmt.Sprintf(multipleDefNoIncLineFmt, n, d))
			} else {
				b.WriteString(fmt.Sprintf(multipleDefLineFmt, n, d, strings.Join(inc, " and ")))
			}
		}
	}
	return fmt.Sprintf(multipleDefFmt, b.String())
}

// objectFuncDefs: the functions the workspace's object files define (nm -C, types T t W w)
// whose base name keep accepts, by base name, each "symbol\x00object\x00type"; nil when nm is
// not here or no object file was written.
func objectFuncDefs(ws string, keep func(base string) bool) map[string][]string {
	nm, err := exec.LookPath("nm")
	if err != nil {
		return nil
	}
	var objs []string
	_ = filepath.WalkDir(ws, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() && e.Name() == ".git" {
			return filepath.SkipDir
		}
		if !e.IsDir() && strings.HasSuffix(p, ".o") && len(objs) < undefinedNearObjMax {
			objs = append(objs, p)
		}
		return nil
	})
	if len(objs) == 0 {
		return nil
	}
	sort.Strings(objs)
	defs := map[string][]string{}
	for i := 0; i < len(objs); i += 200 {
		j := min(i+200, len(objs))
		args := append([]string{"-C", "-A", "--defined-only"}, objs[i:j]...)
		b, _ := exec.Command(nm, args...).Output()
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			// "<object>:<value> <type> <symbol>"
			l := sc.Text()
			c := strings.Index(l, ".o:")
			if c < 0 {
				continue
			}
			obj, rest := l[:c+2], strings.Fields(l[c+3:])
			if len(rest) < 3 || !strings.ContainsAny(rest[1], "TtWw") {
				continue
			}
			sym := strings.Join(rest[2:], " ")
			if b := funcBaseName(sym); keep(b) {
				rel, _ := filepath.Rel(ws, obj)
				defs[b] = append(defs[b], sym+"\x00"+filepath.ToSlash(rel)+"\x00"+rest[1])
			}
		}
	}
	return defs
}
