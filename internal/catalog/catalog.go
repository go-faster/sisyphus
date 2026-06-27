// Package catalog loads and detects services from service_catalog.yaml.
package catalog

import (
	"io"
	"os"
	"strings"

	"github.com/go-faster/errors"
	"gopkg.in/yaml.v3"
)

// Service represents a service in the catalog.
type Service struct {
	Name           string   `yaml:"name"`
	Aliases        []string `yaml:"aliases"`
	Repos          []string `yaml:"repos"`
	Docs           []string `yaml:"docs"`
	JiraProjects   []string `yaml:"jira_projects"`
	JiraComponents []string `yaml:"jira_components"`
	Owners         []string `yaml:"owners"`
	Keywords       []string `yaml:"keywords"`
}

// Catalog holds the service catalog and lookup maps for efficient detection.
type Catalog struct {
	services map[string]*Service

	// Lowercase lookup maps for O(1) lookups.
	repoToService          map[string]string // lowercase repo -> service name
	jiraProjectToService   map[string]string // lowercase project -> service name
	jiraComponentToService map[string]string // lowercase component -> service name
}

// Load loads and parses the service catalog from a file.
func Load(path string) (*Catalog, error) {
	f, err := os.Open(path) // #nosec G304 -- path is from operator configuration
	if err != nil {
		return nil, errors.Wrap(err, "open catalog file")
	}
	defer func() {
		_ = f.Close()
	}()

	return Parse(f)
}

// Parse parses the service catalog from an io.Reader.
func Parse(r io.Reader) (*Catalog, error) {
	var raw struct {
		Services map[string]map[string]any `yaml:"services"`
	}

	if err := yaml.NewDecoder(r).Decode(&raw); err != nil {
		return nil, errors.Wrap(err, "decode catalog YAML")
	}

	catalog := &Catalog{
		services:               make(map[string]*Service),
		repoToService:          make(map[string]string),
		jiraProjectToService:   make(map[string]string),
		jiraComponentToService: make(map[string]string),
	}

	// Parse services from raw YAML map.
	for serviceName, fields := range raw.Services {
		svc := &Service{Name: serviceName}

		// Parse aliases.
		if aliases, ok := fields["aliases"].([]any); ok {
			for _, a := range aliases {
				if s, ok := a.(string); ok {
					svc.Aliases = append(svc.Aliases, s)
				}
			}
		}

		// Parse repos.
		if repos, ok := fields["repos"].([]any); ok {
			for _, r := range repos {
				if s, ok := r.(string); ok {
					svc.Repos = append(svc.Repos, s)
				}
			}
		}

		// Parse docs.
		if docs, ok := fields["docs"].([]any); ok {
			for _, d := range docs {
				if s, ok := d.(string); ok {
					svc.Docs = append(svc.Docs, s)
				}
			}
		}

		// Parse jira_projects.
		if projects, ok := fields["jira_projects"].([]any); ok {
			for _, p := range projects {
				if s, ok := p.(string); ok {
					svc.JiraProjects = append(svc.JiraProjects, s)
				}
			}
		}

		// Parse jira_components.
		if components, ok := fields["jira_components"].([]any); ok {
			for _, c := range components {
				if s, ok := c.(string); ok {
					svc.JiraComponents = append(svc.JiraComponents, s)
				}
			}
		}

		// Parse owners.
		if owners, ok := fields["owners"].([]any); ok {
			for _, o := range owners {
				if s, ok := o.(string); ok {
					svc.Owners = append(svc.Owners, s)
				}
			}
		}

		// Parse keywords.
		if keywords, ok := fields["keywords"].([]any); ok {
			for _, k := range keywords {
				if s, ok := k.(string); ok {
					svc.Keywords = append(svc.Keywords, s)
				}
			}
		}

		catalog.services[serviceName] = svc

		// Build lookup maps (lowercase for case-insensitive lookup).
		for _, repo := range svc.Repos {
			catalog.repoToService[strings.ToLower(repo)] = serviceName
		}
		for _, project := range svc.JiraProjects {
			catalog.jiraProjectToService[strings.ToLower(project)] = serviceName
		}
		for _, component := range svc.JiraComponents {
			catalog.jiraComponentToService[strings.ToLower(component)] = serviceName
		}
	}

	return catalog, nil
}

// ByRepo returns the service name for a given repository, or ("", false) if not found.
func (c *Catalog) ByRepo(repo string) (string, bool) {
	serviceName, ok := c.repoToService[strings.ToLower(repo)]
	return serviceName, ok
}

// ByJiraProject returns the service name for a given Jira project, or ("", false) if not found.
func (c *Catalog) ByJiraProject(project string) (string, bool) {
	serviceName, ok := c.jiraProjectToService[strings.ToLower(project)]
	return serviceName, ok
}

// ByJiraComponent returns the service name for a given Jira component, or ("", false) if not found.
func (c *Catalog) ByJiraComponent(component string) (string, bool) {
	serviceName, ok := c.jiraComponentToService[strings.ToLower(component)]
	return serviceName, ok
}

// Detect performs case-insensitive matching of aliases (strong) and keywords
// (weaker) as whole words or substrings in the text. Returns the best-scoring
// service and its confidence, or ("", 0) if none match.
// Tie-breaking is deterministic by service name.
func (c *Catalog) Detect(text string) (serviceName string, score float64) {
	if text == "" {
		return "", 0
	}

	lowerText := strings.ToLower(text)
	scores := make(map[string]float64)

	for serviceName, svc := range c.services {
		score := 0.0

		// Alias matching (strong): 1.0 per match.
		for _, alias := range svc.Aliases {
			if c.matchesAsWord(lowerText, strings.ToLower(alias)) {
				score = 1.0
				break // Alias match is strong enough.
			}
		}

		// Keyword matching (weaker): accumulate up to 0.9.
		if score < 1.0 {
			keywordMatches := 0
			for _, keyword := range svc.Keywords {
				if strings.Contains(lowerText, strings.ToLower(keyword)) {
					keywordMatches++
				}
			}
			// Accumulate keyword score, capped at 0.9.
			if keywordMatches > 0 {
				keywordScore := float64(keywordMatches) * 0.2 // 0.2 per keyword match
				if keywordScore > 0.9 {
					keywordScore = 0.9
				}
				score = keywordScore
			}
		}

		if score > 0 {
			scores[serviceName] = score
		}
	}

	if len(scores) == 0 {
		return "", 0
	}

	// Find the best score.
	var bestService string
	var bestScore float64
	for svc, sc := range scores {
		if sc > bestScore || (sc == bestScore && (bestService == "" || svc < bestService)) {
			bestService = svc
			bestScore = sc
		}
	}

	return bestService, bestScore
}

// DetectFrom first tries explicit metadata keys (repo, jira_project, jira_component)
// with confidence 1.0, then falls back to Detect(text) for partial matching.
func (c *Catalog) DetectFrom(text string, meta map[string]any) (serviceName string, score float64) {
	// Check explicit metadata keys (highest priority).
	if repo, ok := meta["repo"].(string); ok {
		if serviceName, found := c.ByRepo(repo); found {
			return serviceName, 1.0
		}
	}

	if project, ok := meta["jira_project"].(string); ok {
		if serviceName, found := c.ByJiraProject(project); found {
			return serviceName, 1.0
		}
	}

	if component, ok := meta["jira_component"].(string); ok {
		if serviceName, found := c.ByJiraComponent(component); found {
			return serviceName, 1.0
		}
	}

	// Fall back to text detection.
	return c.Detect(text)
}

// matchesAsWord checks if the word appears in text as a whole word or substring.
// This is a simple substring match for now; can be refined to enforce word boundaries if needed.
func (c *Catalog) matchesAsWord(text, word string) bool {
	return strings.Contains(text, word)
}
