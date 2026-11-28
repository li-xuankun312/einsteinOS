package kernel

// model_guard.go — what an end-to-end run changed in the model's directory, measured and put
// back.
//
// 11/23 the integration's e2e.sh ran pytools/setup_model_ref.py on the model's directory: it
// wrote modeling_qwen3_5_moe.py and configuration_qwen3_5_moe.py there and gave config.json an
// auto_map, so that a load with trust_remote_code would run the code the conversation wrote — in
// the directory the kernel's judge reads the model's own implementation from. The directory is
// read before the run (each small file's content kept), compared after it, and put back.

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// modelGuardKeep: a file of at most this many bytes is kept whole and can be put back.
const modelGuardKeep = 8 << 20

type modelGuardFile struct {
	size  int64
	mtime time.Time
	mode  fs.FileMode
	body  []byte // nil for a file larger than modelGuardKeep
}

// modelGuard: the model's directory as it was before a run.
type modelGuard struct {
	dir   string
	files map[string]modelGuardFile
}

func readModelDir(dir string, keep bool) map[string]modelGuardFile {
	out := map[string]modelGuardFile{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		f := modelGuardFile{size: info.Size(), mtime: info.ModTime(), mode: info.Mode().Perm()}
		if keep && info.Size() <= modelGuardKeep {
			f.body, _ = os.ReadFile(p)
		}
		out[filepath.ToSlash(rel)] = f
		return nil
	})
	return out
}

// guardModelDir reads the model's directory (its link resolved) before a run.
func guardModelDir(dir string) *modelGuard {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	return &modelGuard{dir: dir, files: readModelDir(dir, true)}
}

// restore compares the directory with what it was and puts it back: a file added is removed, a
// small file changed or removed is written again with its time; it returns what the run changed
// ("path (added|changed|removed)", "… — not put back" for a large one).
func (g *modelGuard) restore() []string {
	if g == nil {
		return nil
	}
	now := readModelDir(g.dir, false)
	var out []string
	for rel := range now {
		if _, ok := g.files[rel]; !ok {
			os.Remove(filepath.Join(g.dir, filepath.FromSlash(rel)))
			out = append(out, rel+" (added)")
		}
	}
	for rel, was := range g.files {
		cur, ok := now[rel]
		p := filepath.Join(g.dir, filepath.FromSlash(rel))
		changed := ok && (cur.size != was.size || !cur.mtime.Equal(was.mtime))
		if ok && !changed && was.body != nil {
			if b, err := os.ReadFile(p); err == nil && !bytes.Equal(b, was.body) {
				changed = true
			}
		}
		if ok && !changed {
			continue
		}
		what := "changed"
		if !ok {
			what = "removed"
		}
		if was.body == nil {
			out = append(out, rel+" ("+what+") — not put back: larger than the kernel keeps")
			continue
		}
		os.WriteFile(p, was.body, was.mode)
		os.Chtimes(p, was.mtime, was.mtime)
		out = append(out, rel+" ("+what+")")
	}
	sort.Strings(out)
	return out
}

// %[1]s the script, %[2]s the model's directory, %[3]d files, %[4]s them
const gateE2EModifiesModelFmt = "integration_e2e_modifies_model: %[1]s changed %[3]d file(s) of the model's directory %[2]s, which the kernel put back as they were; the model's own files, and its own implementation the judge runs, are not the port's to write: %[4]s"
