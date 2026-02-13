// Package executor provides task execution with Navigator integration.
package executor

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// TemplateData holds variables for template rendering.
type TemplateData struct {
	// Task fields
	ID                 string
	Title              string
	Slug               string
	Description        string
	AcceptanceCriteria []string

	// SOP fields
	Category string

	// Project fields
	ProjectName string
	TechStack   string

	// Date fields
	Date     string // YYYY-MM-DD
	DateTime string // YYYY-MM-DD HH:MM
	Year     string

	// Custom fields for extensibility
	Custom map[string]string
}

// TemplateRenderer renders Go templates for tasks and SOPs.
type TemplateRenderer struct {
	templatesDir string
	useEmbedded  bool
	funcMap      template.FuncMap
}

// NewTemplateRenderer creates a renderer with template directory.
// If templatesDir is empty, uses embedded templates.
func NewTemplateRenderer(templatesDir string) *TemplateRenderer {
	return &TemplateRenderer{
		templatesDir: templatesDir,
		useEmbedded:  templatesDir == "",
		funcMap: template.FuncMap{
			"date":     func() string { return time.Now().Format("2006-01-02") },
			"datetime": func() string { return time.Now().Format("2006-01-02 15:04") },
			"year":     func() string { return time.Now().Format("2006") },
			"lower":    strings.ToLower,
			"upper":    strings.ToUpper,
			"title":    strings.Title, //nolint:staticcheck // strings.Title is fine for simple cases
			"slugify":  slugify,
			"join":     strings.Join,
		},
	}
}

// RenderTemplate renders a template file with the provided data.
func (r *TemplateRenderer) RenderTemplate(templateName string, data *TemplateData) (string, error) {
	content, err := r.readTemplate(templateName)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}

	// Ensure date fields are populated
	if data.Date == "" {
		data.Date = time.Now().Format("2006-01-02")
	}
	if data.DateTime == "" {
		data.DateTime = time.Now().Format("2006-01-02 15:04")
	}
	if data.Year == "" {
		data.Year = time.Now().Format("2006")
	}

	tmpl, err := template.New(templateName).Funcs(r.funcMap).Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("failed to parse template %s: %w", templateName, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template %s: %w", templateName, err)
	}

	return buf.String(), nil
}

// readTemplate reads a template from disk or embedded FS.
func (r *TemplateRenderer) readTemplate(templateName string) ([]byte, error) {
	if r.useEmbedded {
		return fs.ReadFile(embeddedTemplates, filepath.Join("templates", templateName))
	}
	return os.ReadFile(filepath.Join(r.templatesDir, templateName))
}

// RenderTaskDoc renders the task.md template with task data.
func (r *TemplateRenderer) RenderTaskDoc(id, title, description string, acceptanceCriteria []string) (string, error) {
	data := &TemplateData{
		ID:                 id,
		Title:              title,
		Slug:               slugify(title),
		Description:        description,
		AcceptanceCriteria: acceptanceCriteria,
	}
	return r.RenderTemplate("task.md", data)
}

// RenderSOP renders the sop.md template with SOP data.
func (r *TemplateRenderer) RenderSOP(title, category string) (string, error) {
	data := &TemplateData{
		Title:    title,
		Category: category,
	}
	return r.RenderTemplate("sop.md", data)
}

// RenderString renders a template string (not from file) with data.
func (r *TemplateRenderer) RenderString(templateContent string, data *TemplateData) (string, error) {
	// Ensure date fields are populated
	if data.Date == "" {
		data.Date = time.Now().Format("2006-01-02")
	}
	if data.DateTime == "" {
		data.DateTime = time.Now().Format("2006-01-02 15:04")
	}
	if data.Year == "" {
		data.Year = time.Now().Format("2006")
	}

	tmpl, err := template.New("inline").Funcs(r.funcMap).Parse(templateContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template string: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template string: %w", err)
	}

	return buf.String(), nil
}

// slugify converts a string to a URL-friendly slug.
func slugify(s string) string {
	// Convert to lowercase and replace spaces with hyphens
	slug := strings.ToLower(s)
	slug = strings.ReplaceAll(slug, " ", "-")

	// Remove non-alphanumeric characters except hyphens
	var result strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}

	// Clean up multiple hyphens
	slug = result.String()
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}

	return strings.Trim(slug, "-")
}
