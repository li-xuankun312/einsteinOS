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
	// %[1]s the file of the error, %[2]s the name the macro becomes, %[3]s the macro
	cmdlineMacroByHandFmt = "    %[1]s writes %[2]s itself, the name -D%[3]s= makes of %[3]s:\n"
	// %[1]d the line number, %[2]s the line
	cmdlineMacroByHandLineFmt = "      %[1]d: %[2]s\n"
	cmdlineMacroByHandMax     = 6 // lines of the file given at most
	// %[1]s the macro, %[2]s what it becomes in the end, %[3]s each step with its line
	cmdlineMacroChainFmt = "    %[1]s becomes %[2]s in the end, each -D's name being itself a macro of the scripts: %[3]s\n"
	// %[1]s the macro, %[2]s its name, %[3]s the script line
	cmdlineMacroStepFmt  = "-D%[1]s=%[2]s (%[3]s)"
	cmdlineMacroChainMax = 4 // steps followed at most
)

var macroValueRe = regexp.MustCompile(`-D([A-Za-z_][A-Za-z0-9_]*)=([A-Za-z_][A-Za-z0-9_]*)`)

// macroValue: the name a script line's -D<macro>=<name> makes of the macro ("" if none).
func macroValue(line, macro string) string {
	for _, m := range macroValueRe.FindAllStringSubmatch(line, -1) {
		if m[1] == macro {
			return m[2]
		}
	}
	return ""
}

// writtenByHand: the lines of a source naming name, not in a // comment.
func writtenByHand(path, name string) []scriptLine {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	var out []scriptLine
	for i, l := range strings.Split(string(b), "\n") {
		code := l
		if j := strings.Index(code, "//"); j >= 0 {
			code = code[:j]
		}
		if re.MatchString(code) {
			out = append(out, scriptLine{n: i + 1, text: strings.TrimSpace(l)})
		}
	}
	return out
}

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
			// a shell comment defines nothing: 11/25 16:47:21 build.sh:26 and :28, two comments
			// naming -Dmain=, were given as definitions beside :147
			if strings.HasPrefix(strings.TrimSpace(sc.Text()), "#") {
				continue
			}
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
		// a -D's name that is itself a -D of the scripts: 11/25 16:47:21 -Dmain=e2e_embed_main
		// (build.sh:147) and -De2e_embed_main=e2e_embed_main_ (build.sh:31) made both main and
		// the hand-written e2e_embed_main into e2e_embed_main_, and each was given alone
		if chain := macroChain(ws, scripts, u.macro, defs); chain != "" {
			b.WriteString(chain)
		}
		// the name the rename makes, written in the file by hand as well: 11/25 12:32:45
		// embed_parity.cu:133 wrote e2e_embed_main() beside the main -Dmain= renamed into it
		seenVal := map[string]bool{}
		for _, d := range defs {
			v := macroValue(d.text, u.macro)
			if v == "" || seenVal[v] {
				continue
			}
			seenVal[v] = true
			hand := writtenByHand(filepath.Join(ws, u.file), v)
			if len(hand) == 0 {
				continue
			}
			b.WriteString(fmt.Sprintf(cmdlineMacroByHandFmt, u.file, v, u.macro))
			for i, h := range hand {
				if i == cmdlineMacroByHandMax {
					break
				}
				b.WriteString(fmt.Sprintf(cmdlineMacroByHandLineFmt, h.n, h.text))
			}
		}
	}
	return fmt.Sprintf(cmdlineMacroFmt, b.String())
}

// macroChain: what macro becomes when the name a -D gives it is itself a -D of the scripts,
// followed step by step; "" when it is one step.
func macroChain(ws string, scripts []string, macro string, defs []scriptLine) string {
	var steps []string
	cur, seen := macro, map[string]bool{macro: true}
	lines := defs
	for len(steps) < cmdlineMacroChainMax {
		next, at := "", ""
		for _, d := range lines {
			if v := macroValue(d.text, cur); v != "" {
				next, at = v, fmt.Sprintf("%s:%d", d.script, d.n)
				break
			}
		}
		if next == "" || seen[next] {
			break
		}
		steps = append(steps, fmt.Sprintf(cmdlineMacroStepFmt, cur, next, at))
		seen[next] = true
		cur = next
		lines = macroDefLines(ws, scripts, cur)
	}
	if len(steps) < 2 {
		return ""
	}
	return fmt.Sprintf(cmdlineMacroChainFmt, macro, cur, strings.Join(steps, ", then "))
}
