package vikingdb

import (
	"encoding/json"
	"strings"
)

type VikingDBDriver struct {
	memory *MemoryService
}

func NewVikingDBDriver(engine *Engine, pdb *ProjectDB) *VikingDBDriver {
	return &VikingDBDriver{
		memory: NewMemoryService(engine, pdb),
	}
}

func (d *VikingDBDriver) Match(toolName string) bool {
	n := strings.ToLower(toolName)
	return n == "search_project_memory" ||
		n == "project_status" ||
		n == "load_memory" ||
		n == "search_memory"
}

func (d *VikingDBDriver) Execute(toolName string, inputJSON string, workDir string) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(inputJSON), &args); err != nil {
		return "[vikingdb: invalid input]"
	}
	result, err := d.memory.HandleToolCall(toolName, args)
	if err != nil {
		return "[vikingdb: " + err.Error() + "]"
	}
	return result
}

func (d *VikingDBDriver) MemoryService() *MemoryService {
	return d.memory
}
