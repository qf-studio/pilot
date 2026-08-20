package config

import "fmt"

// ContractDependency declares that this project's implementation depends on
// a contract (interface/schema/API shape) defined in another repository
// (GH-5010). Executor and wiring layers consume this to know which external
// files to check for drift before trusting a contract as stable — e.g. a
// console project depending on pilot's gateway API contract files.
type ContractDependency struct {
	// Owner is the GitHub org/user that owns the dependency repo (required).
	Owner string `yaml:"owner"`
	// Repo is the dependency repo name (required).
	Repo string `yaml:"repo"`
	// ContractFiles lists the paths (within Repo) that define the contract
	// this project depends on — e.g. OpenAPI specs, generated types, schema
	// files. Must contain at least one entry.
	ContractFiles []string `yaml:"contract_files"`
	// Ref is the git ref (branch, tag, or SHA) to read ContractFiles from.
	// Optional — an empty Ref lets the caller fall back to the dependency
	// repo's default branch.
	Ref string `yaml:"ref,omitempty"`
}

// Validate checks that a ContractDependency has all required fields set.
// Owner, Repo, and at least one ContractFiles entry are mandatory; Ref is
// optional.
func (d *ContractDependency) Validate() error {
	if d == nil {
		return nil
	}
	if d.Owner == "" {
		return fmt.Errorf("owner is required")
	}
	if d.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	if len(d.ContractFiles) == 0 {
		return fmt.Errorf("contract_files must contain at least one entry")
	}
	return nil
}
