package kernel

// macro_origin.go — where a macro the compiler says came from the command line is defined.
//
// 11/25 12:32:45 and 12:47:48 tests/k39_integration/embed_parity.cu:147 failed with
// "redefinition of 'e2e_embed_main'", the compiler's note saying "<command line>:3:14: note:
// expanded from macro 'main'". The -Dmain=e2e_embed_main was build.sh's, lines 140 and 189,
// and nothing the conversation was given named them: the embed conversation (b10706e5) took
// the rename to be unreliable on the toolchain and wrote e2e_embed_main by hand beside it.

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	cmdlineMacroRe = regexp.MustCompile(`<command line>:\d+:\d+: note: expanded from macro '([A-Za-z_][A-Za-z0-9_]*)'`)
	compileErrAtRe = regexp.MustCompile(`([A-Za-z0-9_./+-]+\.(?:cu|cuh|cpp|cc|cxx|c|hpp|h)):\d+:\d+: error:`)
)

const (
	cmdlineMacroLinesMax = 6 // script lines given for one macro at most
	// %[1]s the lines
	cmdlineMacroFmt = "[WHERE THE MACROS THE COMPILER SAYS CAME FROM THE COMMAND LINE ARE DEFINED — measured from the build scripts]\n%[1]s"
	// %[1]s the macro, %[2]s the file whose error the note follows
	cmdlineMacroHeadFmt = "  %[1]s (the note after the error in %[2]s):\n"
	// %[1]s the script, %[2]d the line number, %[3]s the line
	cmdlineMacroLineFmt = "    %[1]s:%[2]d: %[3]s\n"
	// %[1]s the macro
	cmdlineMacroNoneFmt = "    no build script of the workspace has -D%[1]s\n"
)

// cmdlineMacroUse: a macro a compiler note says came from the command line, and the file of
// the error that note follows.
type cmdlineMacroUse struct{ macro, file string }

// cmdlineMacroUses: the macros the notes of out say came from the command line, each once,
// with the file of the error line before its note.
func cmdlineMacroUses(out string) []cmdlineMacroUse {
	var uses []cmdlineMacroUse
	seen := map[cmdlineMacroUse]bool{}
	file := ""
	for _, l := range strings.Split(out, "\n") {
		if m := compileErrAtRe.FindStringSubmatch(l); m != nil {
			file = filepath.ToSlash(m[1])
			continue
		}
		m := cmdlineMacroRe.FindStringSubmatch(l)
		if m == nil || file == "" {
			continue
		}
		u := cmdlineMacroUse{macro: m[1], file: file}
		if !seen[u] {
			seen[u] = true
			uses = append(uses, u)
		}
	}
	return uses
}

// buildScripts: build.sh and the shell scripts under .einsteinOS/ — what a build.sh runs.
func buildScripts(ws, build string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(rel string) {
		rel = filepath.ToSlash(rel)
		if rel != "" && !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	if build != "" {
		if filepath.IsAbs(build) {
			if r, err := filepath.Rel(ws, build); err == nil {
				build = r
			}
		}
		add(build)
	}
	_ = filepath.WalkDir(filepath.Join(ws, ".einsteinOS"), func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			return nil
		}
		if r, err := filepath.Rel(ws, p); err == nil {
			add(r)
		}
		return nil
	})
	return out
}

// scriptLine: a line of a build script.
type scriptLine struct {
	script string
	n      int
	text   string
}

// macroDefLines: the lines of the scripts defining -D<macro> (with or without a value).
func macroDefLines(ws string, scripts []string, macro string) []scriptLine {
	re := regexp.MustCompile(`(^|[\s"'])-D` + regexp.QuoteMeta(macro) + `(=|[\s"']|$)`)
	var out []scriptLine
	for _, s := range scripts {
		f, err := os.Open(filepath.Join(ws, s))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for n := 1; sc.Scan(); n++ {
			if re.MatchString(sc.Text()) {
				out = append(out, scriptLine{script: s, n: n, text: strings.TrimSpace(sc.Text())})
			}
		}
		f.Close()
	}
	return out
}

// cmdlineMacroNear: for each macro the compiler's notes say came from the command line, the
// lines of build.sh and the scripts it runs that define it — those naming the file of the
// error first, when some do.
func cmdlineMacroNear(ws, build, out string) string {
	uses := cmdlineMacroUses(out)
	if len(uses) == 0 {
		return ""
	}
	scripts := buildScripts(ws, build)
	var b strings.Builder
	for _, u := range uses {
		b.WriteString(fmt.Sprintf(cmdlineMacroHeadFmt, u.macro, u.file))
		defs := macroDefLines(ws, scripts, u.macro)
		if len(defs) == 0 {
			b.WriteString(fmt.Sprintf(cmdlineMacroNoneFmt, u.macro))
			continue
		}
		// a -Dmain=… on every test's line: those compiling the file of the error
		var named []scriptLine
		for _, d := range defs {
			if strings.Contains(d.text, filepath.Base(u.file)) {
				named = append(named, d)
			}
		}
		if len(named) > 0 {
			defs = named
		}
		for i, d := range defs {
			if i == cmdlineMacroLinesMax {
				break
			}
			b.WriteString(fmt.Sprintf(cmdlineMacroLineFmt, d.script, d.n, d.text))
		}
	}
	return fmt.Sprintf(cmdlineMacroFmt, b.String())
}
