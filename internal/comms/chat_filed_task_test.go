package comms

import "testing"

// TestChatFiledTask_BaseBranchFromProjectConfig covers GH-5695: a chat-filed
// task takes its base branch from the project config (branch_from, else
// default_branch) and leaves it empty when nothing is configured, instead of
// hardcoding "main".
func TestChatFiledTask_BaseBranchFromProjectConfig(t *testing.T) {
	tests := []struct {
		name     string
		project  *ProjectInfo
		createPR bool
		want     string
	}{
		{name: "branch_from", project: &ProjectInfo{Path: "/work/proj", BranchFrom: "mainline"}, createPR: true, want: "mainline"},
		{name: "default_branch", project: &ProjectInfo{Path: "/work/proj", DefaultBranch: "trunk"}, createPR: true, want: "trunk"},
		{name: "branch_from beats default_branch", project: &ProjectInfo{Path: "/work/proj", BranchFrom: "mainline", DefaultBranch: "trunk"}, createPR: true, want: "mainline"},
		{name: "no config", project: &ProjectInfo{Path: "/work/proj"}, createPR: true, want: ""},
		{name: "unregistered project", project: &ProjectInfo{Path: "/other"}, createPR: true, want: ""},
		{name: "no PR leaves base empty", project: &ProjectInfo{Path: "/work/proj", BranchFrom: "mainline"}, createPR: false, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(&HandlerConfig{
				Messenger:    &handlerMock{},
				ProjectPath:  "/work/proj",
				Projects:     stubProjects{list: []*ProjectInfo{tt.project}},
				TaskIDPrefix: "TEST",
			})

			task := h.buildChatTask("ctx", "TEST-1", "do a thing", tt.createPR, "")
			if task.BaseBranch != tt.want {
				t.Errorf("BaseBranch = %q, want %q", task.BaseBranch, tt.want)
			}
		})
	}

	t.Run("nil project source", func(t *testing.T) {
		h := NewHandler(&HandlerConfig{Messenger: &handlerMock{}, ProjectPath: "/work/proj", TaskIDPrefix: "TEST"})
		if task := h.buildChatTask("ctx", "TEST-1", "do a thing", true, ""); task.BaseBranch != "" {
			t.Errorf("BaseBranch = %q, want empty", task.BaseBranch)
		}
	})
}
