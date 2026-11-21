package kernel

// diag_repo.go — the repository the records of this run go to.

import "sync"

var (
	diagRepoMu sync.Mutex
	diagRepo   string
)

// SetDiagRepo names the repository the records of this run go to (the dev repository).
func SetDiagRepo(ownerRepo string) {
	diagRepoMu.Lock()
	diagRepo = ownerRepo
	diagRepoMu.Unlock()
}

// DiagRepo is the repository records go to; "" until a dev run names it.
func DiagRepo() string {
	diagRepoMu.Lock()
	defer diagRepoMu.Unlock()
	return diagRepo
}
