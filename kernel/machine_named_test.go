package kernel

import (
	"strings"
	"testing"
	"time"
)

// 11/23 the integration's e2e.sh ran pytools/patch_import_utils.py, whose SEARCH_BASES list
// /usr/local/lib/python3.10/site-packages, and the change to transformers was measured only after
// it was made: the lines naming the machine's package directories, in the scripts and the files
// they run, are read before any of them runs, and the verification runs none of them
func TestScriptsNamingTheMachinesPackagesAreNotRun(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/verify/uint/build.sh": "#!/bin/bash\n# site-packages in a comment is not a line run\nmkdir -p build\n",
		".einsteinOS/verify/uint/test.sh":  "#!/bin/bash\ntrue\n",
		".einsteinOS/verify/uint/e2e.sh":   "#!/bin/bash\npython3.10 \"$ROOT/pytools/patch_import_utils.py\" || true\npython3.10 \"$ROOT/pytools/e2e_infer.py\"\n",
		"pytools/patch_import_utils.py":    "import os\nSEARCH_BASES = [\n    '/usr/local/lib/python3.10/site-packages',\n]\n",
		"pytools/e2e_infer.py":             "import site\nprint('generated:', 1)\n",
	})
	got := scriptsNamingMachinePackages(ws, "uint")
	if len(got) != 1 || got[0] != "pytools/patch_import_utils.py:3: '/usr/local/lib/python3.10/site-packages'," {
		t.Fatalf("%q", got)
	}
	res := runUnitVerifyAlone(ws, "uint", "", UnitRequirements{}, nil, time.Minute, time.Minute)
	if res.Gate != "verify_names_machine_packages" || res.Compile != "skipped" || !strings.Contains(res.GateText, "patch_import_utils.py:3") {
		t.Fatalf("%+v", res)
	}
	putTree(t, ws, map[string]string{"pytools/patch_import_utils.py": "print('nothing of the machine')\n"})
	if got := scriptsNamingMachinePackages(ws, "uint"); len(got) != 0 {
		t.Fatalf("%q", got)
	}
}
