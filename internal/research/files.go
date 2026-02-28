package research

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ResearchFile describes a single research file in the research directory.
type ResearchFile struct {
	Category string
	Name     string // without .md extension
	Path     string // full absolute path
	Modified time.Time
	Size     int64
}

// ListResearchFiles returns all .md files across all category directories,
// sorted by category then name.
func ListResearchFiles(researchDir string) ([]ResearchFile, error) {
	entries, err := os.ReadDir(researchDir)
	if err != nil {
		return nil, err
	}

	var files []ResearchFile
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] == '.' {
			continue
		}
		catFiles, err := ListCategoryFiles(researchDir, e.Name())
		if err != nil {
			continue
		}
		files = append(files, catFiles...)
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Category != files[j].Category {
			return files[i].Category < files[j].Category
		}
		return files[i].Name < files[j].Name
	})

	return files, nil
}

// ListCategories returns the names of non-hidden top-level directories
// in the research directory, sorted alphabetically.
func ListCategories(researchDir string) ([]string, error) {
	entries, err := os.ReadDir(researchDir)
	if err != nil {
		return nil, err
	}

	var cats []string
	for _, e := range entries {
		if e.IsDir() && e.Name()[0] != '.' {
			cats = append(cats, e.Name())
		}
	}
	sort.Strings(cats)
	return cats, nil
}

// ListCategoryFiles returns all .md files within a specific category directory,
// sorted by name.
func ListCategoryFiles(researchDir, category string) ([]ResearchFile, error) {
	catDir := filepath.Join(researchDir, category)
	entries, err := os.ReadDir(catDir)
	if err != nil {
		return nil, err
	}

	var files []ResearchFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		var mod time.Time
		var size int64
		if err == nil {
			mod = info.ModTime()
			size = info.Size()
		}
		files = append(files, ResearchFile{
			Category: category,
			Name:     strings.TrimSuffix(e.Name(), ".md"),
			Path:     filepath.Join(catDir, e.Name()),
			Modified: mod,
			Size:     size,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

	return files, nil
}
