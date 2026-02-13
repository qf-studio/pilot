package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/teams"
)

func newTeamCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team",
		Short: "Manage teams and permissions",
		Long: `Manage teams, members, and role-based access control.

Teams allow multiple users to collaborate on Pilot with different permission levels:
  - owner:     Full access, can delete team
  - admin:     Manage members and projects
  - developer: Execute tasks on assigned projects
  - viewer:    Read-only access`,
	}

	cmd.AddCommand(
		newTeamCreateCmd(),
		newTeamListCmd(),
		newTeamShowCmd(),
		newTeamDeleteCmd(),
		newTeamMemberCmd(),
		newTeamProjectCmd(),
		newTeamAuditCmd(),
	)

	return cmd
}

func newTeamCreateCmd() *cobra.Command {
	var ownerEmail string

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if ownerEmail == "" {
				return fmt.Errorf("owner email is required (use --owner)")
			}

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, owner, err := service.CreateTeam(name, ownerEmail)
			if err != nil {
				return fmt.Errorf("failed to create team: %w", err)
			}

			fmt.Println("✅ Team created successfully!")
			fmt.Println()
			fmt.Printf("   Team ID:   %s\n", team.ID)
			fmt.Printf("   Name:      %s\n", team.Name)
			fmt.Printf("   Owner:     %s (%s)\n", ownerEmail, owner.ID)
			fmt.Println()
			fmt.Println("💡 Add members with:")
			fmt.Printf("   pilot team member add %s <email> --role developer\n", team.ID[:8])

			return nil
		},
	}

	cmd.Flags().StringVar(&ownerEmail, "owner", "", "Owner email address (required)")
	_ = cmd.MarkFlagRequired("owner")

	return cmd
}

func newTeamListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all teams",
		RunE: func(cmd *cobra.Command, args []string) error {
			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			teamList, err := service.ListTeams()
			if err != nil {
				return fmt.Errorf("failed to list teams: %w", err)
			}

			if len(teamList) == 0 {
				fmt.Println("No teams found.")
				fmt.Println()
				fmt.Println("Create one with:")
				fmt.Println("   pilot team create \"My Team\" --owner you@example.com")
				return nil
			}

			fmt.Printf("Found %d team(s):\n\n", len(teamList))

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tCREATED")
			_, _ = fmt.Fprintln(w, "──\t────\t───────")

			for _, t := range teamList {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n",
					t.ID[:8],
					t.Name,
					t.CreatedAt.Format("2006-01-02"),
				)
			}
			_ = w.Flush()

			return nil
		},
	}
}

func newTeamShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [team-id]",
		Short: "Show team details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			// Try to find by partial ID or name
			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			members, err := service.ListMembers(team.ID)
			if err != nil {
				return fmt.Errorf("failed to get members: %w", err)
			}

			fmt.Printf("📋 Team: %s\n", team.Name)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("   ID:         %s\n", team.ID)
			fmt.Printf("   Created:    %s\n", team.CreatedAt.Format("2006-01-02 15:04"))
			fmt.Printf("   Max Tasks:  %d concurrent\n", team.Settings.MaxConcurrentTasks)
			fmt.Println()

			fmt.Printf("👥 Members (%d):\n", len(members))
			if len(members) == 0 {
				fmt.Println("   (no members)")
			} else {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				_, _ = fmt.Fprintln(w, "   ID\tEMAIL\tROLE\tPROJECTS")
				for _, m := range members {
					projects := "all"
					if len(m.Projects) > 0 {
						projects = strings.Join(m.Projects, ", ")
					}
					_, _ = fmt.Fprintf(w, "   %s\t%s\t%s\t%s\n",
						m.ID[:8],
						m.Email,
						m.Role,
						projects,
					)
				}
				_ = w.Flush()
			}

			// Show project access entries
			accesses, err := service.ListProjectAccess(team.ID)
			if err == nil && len(accesses) > 0 {
				fmt.Println()
				fmt.Printf("📂 Project Access (%d):\n", len(accesses))
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				_, _ = fmt.Fprintln(w, "   PROJECT PATH\tDEFAULT ROLE")
				for _, a := range accesses {
					_, _ = fmt.Fprintf(w, "   %s\t%s\n", a.ProjectPath, a.DefaultRole)
				}
				_ = w.Flush()
			}

			return nil
		},
	}
}

func newTeamDeleteCmd() *cobra.Command {
	var force bool
	var actorEmail string

	cmd := &cobra.Command{
		Use:   "delete [team-id]",
		Short: "Delete a team (owner only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]

			if actorEmail == "" {
				return fmt.Errorf("actor email required (use --as)")
			}

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			actor, err := service.GetMemberByEmail(team.ID, actorEmail)
			if err != nil || actor == nil {
				return fmt.Errorf("you are not a member of this team")
			}

			if !force {
				fmt.Printf("⚠️  This will permanently delete team '%s' and all associated data.\n", team.Name)
				fmt.Print("   Type 'yes' to confirm: ")
				var confirm string
				_, _ = fmt.Scanln(&confirm)
				if confirm != "yes" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			if err := service.DeleteTeam(team.ID, actor.ID); err != nil {
				return fmt.Errorf("failed to delete team: %w", err)
			}

			fmt.Printf("✅ Team '%s' deleted.\n", team.Name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation")
	cmd.Flags().StringVar(&actorEmail, "as", "", "Your email (must be team owner)")
	_ = cmd.MarkFlagRequired("as")

	return cmd
}

func newTeamMemberCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "member",
		Short: "Manage team members",
	}

	cmd.AddCommand(
		newTeamMemberAddCmd(),
		newTeamMemberRemoveCmd(),
		newTeamMemberRoleCmd(),
		newTeamMemberListCmd(),
	)

	return cmd
}

func newTeamMemberAddCmd() *cobra.Command {
	var (
		role       string
		projects   []string
		actorEmail string
	)

	cmd := &cobra.Command{
		Use:   "add [team-id] [email]",
		Short: "Add a member to a team",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]
			email := args[1]

			if actorEmail == "" {
				return fmt.Errorf("actor email required (use --as)")
			}

			if !teams.Role(role).IsValid() {
				return fmt.Errorf("invalid role: %s (use owner, admin, developer, or viewer)", role)
			}

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			actor, err := service.GetMemberByEmail(team.ID, actorEmail)
			if err != nil || actor == nil {
				return fmt.Errorf("you are not a member of this team")
			}

			member, err := service.AddMember(team.ID, actor.ID, email, teams.Role(role), projects)
			if err != nil {
				return fmt.Errorf("failed to add member: %w", err)
			}

			fmt.Printf("✅ Added %s to team '%s' as %s\n", email, team.Name, role)
			fmt.Printf("   Member ID: %s\n", member.ID[:8])

			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", "developer", "Role: owner, admin, developer, viewer")
	cmd.Flags().StringSliceVar(&projects, "projects", nil, "Restrict to specific projects (empty = all)")
	cmd.Flags().StringVar(&actorEmail, "as", "", "Your email (must have manage_members permission)")
	_ = cmd.MarkFlagRequired("as")

	return cmd
}

func newTeamMemberRemoveCmd() *cobra.Command {
	var actorEmail string

	cmd := &cobra.Command{
		Use:   "remove [team-id] [member-email]",
		Short: "Remove a member from a team",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]
			memberEmail := args[1]

			if actorEmail == "" {
				return fmt.Errorf("actor email required (use --as)")
			}

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			actor, err := service.GetMemberByEmail(team.ID, actorEmail)
			if err != nil || actor == nil {
				return fmt.Errorf("you are not a member of this team")
			}

			member, err := service.GetMemberByEmail(team.ID, memberEmail)
			if err != nil || member == nil {
				return fmt.Errorf("member not found: %s", memberEmail)
			}

			if err := service.RemoveMember(team.ID, actor.ID, member.ID); err != nil {
				return fmt.Errorf("failed to remove member: %w", err)
			}

			fmt.Printf("✅ Removed %s from team '%s'\n", memberEmail, team.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&actorEmail, "as", "", "Your email (must have manage_members permission)")
	_ = cmd.MarkFlagRequired("as")

	return cmd
}

func newTeamMemberRoleCmd() *cobra.Command {
	var actorEmail string

	cmd := &cobra.Command{
		Use:   "role [team-id] [member-email] [new-role]",
		Short: "Change a member's role",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]
			memberEmail := args[1]
			newRole := args[2]

			if actorEmail == "" {
				return fmt.Errorf("actor email required (use --as)")
			}

			if !teams.Role(newRole).IsValid() {
				return fmt.Errorf("invalid role: %s (use owner, admin, developer, or viewer)", newRole)
			}

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			actor, err := service.GetMemberByEmail(team.ID, actorEmail)
			if err != nil || actor == nil {
				return fmt.Errorf("you are not a member of this team")
			}

			member, err := service.GetMemberByEmail(team.ID, memberEmail)
			if err != nil || member == nil {
				return fmt.Errorf("member not found: %s", memberEmail)
			}

			if err := service.UpdateMemberRole(team.ID, actor.ID, member.ID, teams.Role(newRole)); err != nil {
				return fmt.Errorf("failed to update role: %w", err)
			}

			fmt.Printf("✅ Changed %s role from %s to %s\n", memberEmail, member.Role, newRole)
			return nil
		},
	}

	cmd.Flags().StringVar(&actorEmail, "as", "", "Your email (must have manage_members permission)")
	_ = cmd.MarkFlagRequired("as")

	return cmd
}

func newTeamMemberListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [team-id]",
		Short: "List members of a team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			members, err := service.ListMembers(team.ID)
			if err != nil {
				return fmt.Errorf("failed to list members: %w", err)
			}

			if len(members) == 0 {
				fmt.Printf("No members in team '%s'.\n", team.Name)
				fmt.Println()
				fmt.Println("Add a member with:")
				fmt.Printf("   pilot team member add %s <email> --role developer --as you@example.com\n", team.ID[:8])
				return nil
			}

			fmt.Printf("Members of '%s' (%d total):\n\n", team.Name, len(members))

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tEMAIL\tROLE\tPROJECTS")
			_, _ = fmt.Fprintln(w, "──\t─────\t────\t────────")

			for _, m := range members {
				projects := "all"
				if len(m.Projects) > 0 {
					projects = strings.Join(m.Projects, ", ")
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					m.ID[:8],
					m.Email,
					m.Role,
					projects,
				)
			}
			_ = w.Flush()

			return nil
		},
	}
}

func newTeamAuditCmd() *cobra.Command {
	var (
		limit      int
		actorEmail string
	)

	cmd := &cobra.Command{
		Use:   "audit [team-id]",
		Short: "View team audit log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]

			if actorEmail == "" {
				return fmt.Errorf("actor email required (use --as)")
			}

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			actor, err := service.GetMemberByEmail(team.ID, actorEmail)
			if err != nil || actor == nil {
				return fmt.Errorf("you are not a member of this team")
			}

			entries, err := service.GetAuditLog(team.ID, actor.ID, limit)
			if err != nil {
				return fmt.Errorf("failed to get audit log: %w", err)
			}

			if len(entries) == 0 {
				fmt.Println("No audit entries found.")
				return nil
			}

			fmt.Printf("📜 Audit Log for '%s' (showing %d entries)\n", team.Name, len(entries))
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, e := range entries {
				fmt.Printf("[%s] %s performed %s on %s",
					e.CreatedAt.Format("2006-01-02 15:04"),
					e.ActorEmail,
					e.Action,
					e.Resource,
				)
				if e.ResourceID != "" {
					fmt.Printf(" (%s)", e.ResourceID[:8])
				}
				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum entries to show")
	cmd.Flags().StringVar(&actorEmail, "as", "", "Your email (must have view_audit_log permission)")
	_ = cmd.MarkFlagRequired("as")

	return cmd
}

func newTeamProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage team project access",
	}

	cmd.AddCommand(
		newTeamProjectSetCmd(),
		newTeamProjectRemoveCmd(),
		newTeamProjectListCmd(),
	)

	return cmd
}

func newTeamProjectSetCmd() *cobra.Command {
	var (
		defaultRole string
		actorEmail  string
	)

	cmd := &cobra.Command{
		Use:   "set [team-id] [project-path]",
		Short: "Set project access with a default role",
		Long: `Set or update project access for a team with a default role.

The default role determines the minimum permission level for all team members
on the specified project. Members may still have higher permissions based on
their individual role.

Example:
  pilot team project set abc123 /path/to/project --role developer --as owner@example.com`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]
			projectPath := args[1]

			if actorEmail == "" {
				return fmt.Errorf("actor email required (use --as)")
			}

			if !teams.Role(defaultRole).IsValid() {
				return fmt.Errorf("invalid role: %s (use owner, admin, developer, or viewer)", defaultRole)
			}

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			actor, err := service.GetMemberByEmail(team.ID, actorEmail)
			if err != nil || actor == nil {
				return fmt.Errorf("you are not a member of this team")
			}

			if err := service.SetProjectAccess(team.ID, actor.ID, projectPath, teams.Role(defaultRole)); err != nil {
				return fmt.Errorf("failed to set project access: %w", err)
			}

			fmt.Printf("✅ Set project access for '%s' on team '%s' (default role: %s)\n", projectPath, team.Name, defaultRole)
			return nil
		},
	}

	cmd.Flags().StringVar(&defaultRole, "role", "developer", "Default role: owner, admin, developer, viewer")
	cmd.Flags().StringVar(&actorEmail, "as", "", "Your email (must have manage_projects permission)")
	_ = cmd.MarkFlagRequired("as")

	return cmd
}

func newTeamProjectRemoveCmd() *cobra.Command {
	var actorEmail string

	cmd := &cobra.Command{
		Use:   "remove [team-id] [project-path]",
		Short: "Remove project access from a team",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]
			projectPath := args[1]

			if actorEmail == "" {
				return fmt.Errorf("actor email required (use --as)")
			}

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			actor, err := service.GetMemberByEmail(team.ID, actorEmail)
			if err != nil || actor == nil {
				return fmt.Errorf("you are not a member of this team")
			}

			if err := service.RemoveProjectAccess(team.ID, actor.ID, projectPath); err != nil {
				return fmt.Errorf("failed to remove project access: %w", err)
			}

			fmt.Printf("✅ Removed project access for '%s' from team '%s'\n", projectPath, team.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&actorEmail, "as", "", "Your email (must have manage_projects permission)")
	_ = cmd.MarkFlagRequired("as")

	return cmd
}

func newTeamProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [team-id]",
		Short: "List project access entries for a team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID := args[0]

			service, cleanup, err := getTeamService()
			if err != nil {
				return err
			}
			defer cleanup()

			team, err := findTeam(service, teamID)
			if err != nil {
				return err
			}

			accesses, err := service.ListProjectAccess(team.ID)
			if err != nil {
				return fmt.Errorf("failed to list project access: %w", err)
			}

			if len(accesses) == 0 {
				fmt.Printf("No project access entries for team '%s'.\n", team.Name)
				fmt.Println()
				fmt.Println("Add one with:")
				fmt.Printf("   pilot team project set %s /path/to/project --role developer --as you@example.com\n", team.ID[:8])
				return nil
			}

			fmt.Printf("📂 Project Access for '%s' (%d entries):\n\n", team.Name, len(accesses))

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "PROJECT PATH\tDEFAULT ROLE")
			_, _ = fmt.Fprintln(w, "────────────\t────────────")

			for _, a := range accesses {
				_, _ = fmt.Fprintf(w, "%s\t%s\n", a.ProjectPath, a.DefaultRole)
			}
			_ = w.Flush()

			return nil
		},
	}
}

// Helper functions

func getTeamService() (*teams.Service, func(), error) {
	configPath := cfgFile
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Open database
	dbPath := filepath.Join(cfg.Memory.Path, "pilot.db")
	if err := os.MkdirAll(cfg.Memory.Path, 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open database: %w", err)
	}

	store, err := teams.NewStore(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("failed to create team store: %w", err)
	}

	service := teams.NewService(store)

	cleanup := func() {
		_ = db.Close()
	}

	return service, cleanup, nil
}

func findTeam(service *teams.Service, idOrName string) (*teams.Team, error) {
	// Try by ID first
	team, err := service.GetTeam(idOrName)
	if err == nil && team != nil {
		return team, nil
	}

	// Try by name
	team, err = service.GetTeamByName(idOrName)
	if err == nil && team != nil {
		return team, nil
	}

	// Try partial ID match
	allTeams, err := service.ListTeams()
	if err != nil {
		return nil, fmt.Errorf("failed to list teams: %w", err)
	}

	for _, t := range allTeams {
		if strings.HasPrefix(t.ID, idOrName) {
			return t, nil
		}
	}

	return nil, fmt.Errorf("team not found: %s", idOrName)
}
