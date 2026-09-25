package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marklubin/researchguy/internal/config"
	"gopkg.in/yaml.v3"
)

// workspaceRegistry is the subset of grepai's workspace.yaml we need.
type workspaceRegistry struct {
	Workspaces map[string]struct {
		Projects []struct {
			Name string `yaml:"name"`
			Path string `yaml:"path"`
		} `yaml:"projects"`
	} `yaml:"workspaces"`
}

// ProjectRoots returns project name -> root directory for cfg's grepai
// workspace, read from grepai's workspace registry. With no workspace
// configured it returns an empty map.
func ProjectRoots(cfg *config.Config) (map[string]string, error) {
	roots := map[string]string{}
	if cfg.Grepai.Workspace == "" {
		return roots, nil
	}
	file := cfg.Grepai.WorkspaceFile
	if file == "" {
		file = "~/.grepai/workspace.yaml"
	}
	data, err := os.ReadFile(config.ExpandPath(file))
	if err != nil {
		return roots, fmt.Errorf("reading grepai workspace registry: %w", err)
	}
	var reg workspaceRegistry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return roots, fmt.Errorf("parsing grepai workspace registry: %w", err)
	}
	for _, p := range reg.Workspaces[cfg.Grepai.Workspace].Projects {
		if p.Name != "" && p.Path != "" {
			roots[p.Name] = config.ExpandPath(p.Path)
		}
	}
	return roots, nil
}

// Resolver maps grepai hit paths to the project they came from and the file
// on disk. Workspace hits look like "<workspace>/<project>/<rel>"; plain
// hits are relative to research_dir.
type Resolver struct {
	researchDir     string
	workspace       string
	researchProject string
	roots           map[string]string
}

// NewResolver builds a Resolver for cfg. An unreadable workspace registry
// is not fatal: hits from the research_dir project still resolve, others
// resolve to an empty path.
func NewResolver(cfg *config.Config) *Resolver {
	roots, _ := ProjectRoots(cfg)
	r := &Resolver{
		researchDir:     config.ExpandPath(cfg.ResearchDir),
		workspace:       cfg.Grepai.Workspace,
		researchProject: cfg.Grepai.Project,
		roots:           roots,
	}
	for name, root := range roots {
		if filepath.Clean(root) == filepath.Clean(r.researchDir) {
			r.researchProject = name
		}
	}
	return r
}

// Resolve returns the project name (empty outside workspace mode) and the
// absolute path for a grepai file_path. The path is empty when a workspace
// project's root is unknown.
func (r *Resolver) Resolve(filePath string) (project, abs string) {
	if filepath.IsAbs(filePath) {
		return "", filePath
	}
	if r.workspace != "" {
		if rest, ok := strings.CutPrefix(filePath, r.workspace+"/"); ok {
			if proj, rel, ok := strings.Cut(rest, "/"); ok {
				if root, found := r.roots[proj]; found {
					return proj, filepath.Join(root, filepath.FromSlash(rel))
				}
				if proj == r.researchProject {
					return proj, filepath.Join(r.researchDir, filepath.FromSlash(rel))
				}
				return proj, ""
			}
		}
	}
	return "", filepath.Join(r.researchDir, filepath.FromSlash(filePath))
}

// Label returns a display name for a hit: its path without the workspace
// prefix, e.g. "the_poisoned_well/research/web/001-researcher-1.md".
func (r *Resolver) Label(filePath string) string {
	if r.workspace != "" {
		if rest, ok := strings.CutPrefix(filePath, r.workspace+"/"); ok {
			return rest
		}
	}
	return filePath
}

// Annotate fills Project and Path on each result.
func Annotate(cfg *config.Config, results []SearchResult) []SearchResult {
	r := NewResolver(cfg)
	for i := range results {
		results[i].Project, results[i].Path = r.Resolve(results[i].FilePath)
	}
	return results
}

// resultPath returns the on-disk path for a result, preferring the resolved
// Path and falling back to joining FilePath onto researchDir.
func resultPath(r SearchResult, researchDir string) string {
	if r.Path != "" {
		return r.Path
	}
	if filepath.IsAbs(r.FilePath) {
		return r.FilePath
	}
	return filepath.Join(researchDir, r.FilePath)
}
