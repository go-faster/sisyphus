package catalog

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	yamlContent := `
services:
  billing-api:
    aliases:
      - billing
      - invoices
      - invoice confirmation
      - payments
    repos:
      - group/billing-api
    docs:
      - platform/docs/billing.md
    jira_projects:
      - BILL
    jira_components:
      - Billing
    owners:
      - backend-payments
    keywords:
      - invoice
      - callback
      - payment pending
      - confirmation
  auth-service:
    aliases:
      - auth
      - authentication
      - login
    repos:
      - group/auth-service
    docs:
      - platform/docs/auth.md
    jira_projects:
      - AUTH
    jira_components:
      - Authentication
    owners:
      - backend-auth
    keywords:
      - oauth
      - session
      - token
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Verify services were loaded.
	if catalog.services == nil {
		t.Fatal("services map is nil")
	}

	if len(catalog.services) != 2 {
		t.Errorf("expected 2 services, got %d", len(catalog.services))
	}

	// Verify billing-api service.
	billingAPI, ok := catalog.services["billing-api"]
	if !ok {
		t.Fatal("billing-api service not found")
	}

	if billingAPI.Name != "billing-api" {
		t.Errorf("expected name 'billing-api', got '%s'", billingAPI.Name)
	}

	if len(billingAPI.Aliases) != 4 {
		t.Errorf("expected 4 aliases, got %d", len(billingAPI.Aliases))
	}

	if len(billingAPI.Keywords) != 4 {
		t.Errorf("expected 4 keywords, got %d", len(billingAPI.Keywords))
	}
}

func TestByRepo(t *testing.T) {
	yamlContent := `
services:
  billing-api:
    repos:
      - group/billing-api
      - group/billing-service
  auth-service:
    repos:
      - group/auth-service
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	tests := []struct {
		repo      string
		expected  string
		shouldErr bool
	}{
		{"group/billing-api", "billing-api", false},
		{"GROUP/BILLING-API", "billing-api", false}, // case-insensitive
		{"group/billing-service", "billing-api", false},
		{"group/auth-service", "auth-service", false},
		{"group/unknown", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		service, ok := catalog.ByRepo(tt.repo)
		if tt.shouldErr && ok {
			t.Errorf("ByRepo(%q): expected error, got %q", tt.repo, service)
		}
		if !tt.shouldErr && !ok {
			t.Errorf("ByRepo(%q): expected %q, not found", tt.repo, tt.expected)
		}
		if !tt.shouldErr && ok && service != tt.expected {
			t.Errorf("ByRepo(%q): expected %q, got %q", tt.repo, tt.expected, service)
		}
	}
}

func TestByJiraProject(t *testing.T) {
	yamlContent := `
services:
  billing-api:
    jira_projects:
      - BILL
  auth-service:
    jira_projects:
      - AUTH
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	tests := []struct {
		project   string
		expected  string
		shouldErr bool
	}{
		{"BILL", "billing-api", false},
		{"bill", "billing-api", false}, // case-insensitive
		{"AUTH", "auth-service", false},
		{"UNKNOWN", "", true},
	}

	for _, tt := range tests {
		service, ok := catalog.ByJiraProject(tt.project)
		if tt.shouldErr && ok {
			t.Errorf("ByJiraProject(%q): expected error, got %q", tt.project, service)
		}
		if !tt.shouldErr && !ok {
			t.Errorf("ByJiraProject(%q): expected %q, not found", tt.project, tt.expected)
		}
		if !tt.shouldErr && ok && service != tt.expected {
			t.Errorf("ByJiraProject(%q): expected %q, got %q", tt.project, tt.expected, service)
		}
	}
}

func TestByJiraComponent(t *testing.T) {
	yamlContent := `
services:
  billing-api:
    jira_components:
      - Billing
  auth-service:
    jira_components:
      - Authentication
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	tests := []struct {
		component string
		expected  string
		shouldErr bool
	}{
		{"Billing", "billing-api", false},
		{"billing", "billing-api", false}, // case-insensitive
		{"Authentication", "auth-service", false},
		{"UNKNOWN", "", true},
	}

	for _, tt := range tests {
		service, ok := catalog.ByJiraComponent(tt.component)
		if tt.shouldErr && ok {
			t.Errorf("ByJiraComponent(%q): expected error, got %q", tt.component, service)
		}
		if !tt.shouldErr && !ok {
			t.Errorf("ByJiraComponent(%q): expected %q, not found", tt.component, tt.expected)
		}
		if !tt.shouldErr && ok && service != tt.expected {
			t.Errorf("ByJiraComponent(%q): expected %q, got %q", tt.component, tt.expected, service)
		}
	}
}

func TestDetect(t *testing.T) {
	yamlContent := `
services:
  billing-api:
    aliases:
      - billing
      - invoices
      - payment
    keywords:
      - invoice
      - callback
      - confirmation
  auth-service:
    aliases:
      - auth
      - login
    keywords:
      - oauth
      - session
      - token
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	tests := []struct {
		text            string
		expectedService string
		minConfidence   float64
		maxConfidence   float64
	}{
		// Alias matches (strong, should be 1.0).
		{"I need help with billing", "billing-api", 1.0, 1.0},
		{"I need invoices", "billing-api", 1.0, 1.0},
		{"I forgot my login password", "auth-service", 1.0, 1.0},
		{"Help me set up oauth", "auth-service", 1.0, 1.0},
		{"BILLING is broken", "billing-api", 1.0, 1.0}, // case-insensitive

		// Keyword matches (weaker, should be < 1.0).
		{"Is there a callback for invoice confirmation?", "billing-api", 0.2, 0.9},
		{"Please process this invoice", "billing-api", 0.2, 0.9},

		// No match.
		{"Completely unrelated topic about weather", "", 0.0, 0.0},
		{"", "", 0.0, 0.0},
	}

	for _, tt := range tests {
		service, confidence := catalog.Detect(tt.text)
		if service != tt.expectedService {
			t.Errorf("Detect(%q): expected service %q, got %q", tt.text, tt.expectedService, service)
		}
		if confidence < tt.minConfidence || confidence > tt.maxConfidence {
			t.Errorf("Detect(%q): confidence %f not in range [%f, %f]", tt.text, confidence, tt.minConfidence, tt.maxConfidence)
		}
	}
}

func TestDetectTieBreaking(t *testing.T) {
	// Test that tie-breaking is deterministic by service name.
	yamlContent := `
services:
  service-b:
    aliases:
      - shared
    keywords:
      - keyword1
  service-a:
    aliases:
      - shared
    keywords:
      - keyword1
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Both services match equally, so the one earlier alphabetically wins.
	service, confidence := catalog.Detect("shared keyword1")
	if service != "service-a" {
		t.Errorf("expected service-a (alphabetically first), got %s", service)
	}
	if confidence != 1.0 {
		t.Errorf("expected confidence 1.0 (alias match), got %f", confidence)
	}
}

func TestDetectFrom(t *testing.T) {
	yamlContent := `
services:
  billing-api:
    aliases:
      - billing
    repos:
      - group/billing-api
    jira_projects:
      - BILL
    jira_components:
      - Billing
    keywords:
      - invoice
  auth-service:
    aliases:
      - auth
    repos:
      - group/auth-service
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	tests := []struct {
		text               string
		meta               map[string]any
		expectedService    string
		expectedConfidence float64
	}{
		// Explicit repo match (confidence 1.0).
		{
			"some text",
			map[string]any{"repo": "group/billing-api"},
			"billing-api",
			1.0,
		},
		// Explicit jira_project match (confidence 1.0).
		{
			"some text",
			map[string]any{"jira_project": "BILL"},
			"billing-api",
			1.0,
		},
		// Explicit jira_component match (confidence 1.0).
		{
			"some text",
			map[string]any{"jira_component": "Billing"},
			"billing-api",
			1.0,
		},
		// Fall back to text detection when no explicit metadata.
		{
			"Help with billing",
			map[string]any{},
			"billing-api",
			1.0,
		},
		// Empty metadata falls back to text detection.
		{
			"authentication issue",
			nil,
			"auth-service",
			1.0,
		},
		// Explicit metadata takes priority over text.
		{
			"Help with auth",
			map[string]any{"repo": "group/billing-api"},
			"billing-api",
			1.0,
		},
		// Case-insensitive metadata lookup.
		{
			"some text",
			map[string]any{"repo": "GROUP/BILLING-API"},
			"billing-api",
			1.0,
		},
	}

	for i, tt := range tests {
		service, confidence := catalog.DetectFrom(tt.text, tt.meta)
		if service != tt.expectedService {
			t.Errorf("test %d: DetectFrom expected service %q, got %q", i, tt.expectedService, service)
		}
		if confidence != tt.expectedConfidence {
			t.Errorf("test %d: DetectFrom expected confidence %f, got %f", i, tt.expectedConfidence, confidence)
		}
	}
}

func TestDetectNoMatch(t *testing.T) {
	yamlContent := `
services:
  billing-api:
    aliases:
      - billing
    keywords:
      - invoice
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	service, confidence := catalog.Detect("completely unrelated text about weather")
	if service != "" {
		t.Errorf("expected no match, got service %q", service)
	}
	if confidence != 0 {
		t.Errorf("expected confidence 0, got %f", confidence)
	}
}

func TestEmptyText(t *testing.T) {
	yamlContent := `
services:
  billing-api:
    aliases:
      - billing
`

	catalog, err := Parse(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	service, confidence := catalog.Detect("")
	if service != "" {
		t.Errorf("expected no match for empty text, got service %q", service)
	}
	if confidence != 0 {
		t.Errorf("expected confidence 0 for empty text, got %f", confidence)
	}
}
