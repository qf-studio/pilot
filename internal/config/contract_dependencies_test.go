package config

import (
	"strings"
	"testing"
)

func TestContractDependency_Validate(t *testing.T) {
	tests := []struct {
		name      string
		dep       ContractDependency
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid",
			dep: ContractDependency{
				Owner:         "qf-studio",
				Repo:          "console",
				ContractFiles: []string{"api/openapi.yaml"},
			},
			wantErr: false,
		},
		{
			name: "valid with ref",
			dep: ContractDependency{
				Owner:         "qf-studio",
				Repo:          "console",
				ContractFiles: []string{"api/openapi.yaml", "api/types.ts"},
				Ref:           "main",
			},
			wantErr: false,
		},
		{
			name: "missing owner",
			dep: ContractDependency{
				Repo:          "console",
				ContractFiles: []string{"api/openapi.yaml"},
			},
			wantErr:   true,
			errSubstr: "owner is required",
		},
		{
			name: "missing repo",
			dep: ContractDependency{
				Owner:         "qf-studio",
				ContractFiles: []string{"api/openapi.yaml"},
			},
			wantErr:   true,
			errSubstr: "repo is required",
		},
		{
			name: "empty contract_files",
			dep: ContractDependency{
				Owner: "qf-studio",
				Repo:  "console",
			},
			wantErr:   true,
			errSubstr: "contract_files must contain at least one entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.dep.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errSubstr)
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfig_Validate_ContractDependencies(t *testing.T) {
	tests := []struct {
		name      string
		deps      []ContractDependency
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "nil deps is valid",
			deps:    nil,
			wantErr: false,
		},
		{
			name: "valid deps",
			deps: []ContractDependency{
				{Owner: "qf-studio", Repo: "pilot", ContractFiles: []string{"src/lib/pilotClient.ts"}},
			},
			wantErr: false,
		},
		{
			name: "invalid entry surfaces indexed error",
			deps: []ContractDependency{
				{Owner: "qf-studio", Repo: "pilot", ContractFiles: []string{"src/lib/pilotClient.ts"}},
				{Owner: "", Repo: "console", ContractFiles: []string{"api/types.ts"}},
			},
			wantErr:   true,
			errSubstr: "projects[0].contract_dependencies[1]: owner is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Projects[0].ContractDependencies = tt.deps

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errSubstr)
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
