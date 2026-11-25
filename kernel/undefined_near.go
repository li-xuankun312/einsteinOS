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
	nm, err := exec.LookPath("nm")
	if err != nil {
		return ""
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
		return ""
	}
	sort.Strings(objs)
	defs := map[string][]string{} // base name -> "sym\x00object"
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
			if b := funcBaseName(sym); want[b] != nil {
				rel, _ := filepath.Rel(ws, obj)
				defs[b] = append(defs[b], sym+"\x00"+filepath.ToSlash(rel))
			}
		}
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
				sym, obj, _ := strings.Cut(d, "\x00")
				lb.WriteString(fmt.Sprintf(undefinedNearLineFmt, ref, sym, obj))
				n++
			}
		}
	}
	return fmt.Sprintf(undefinedNearFmt, lb.String())
}
