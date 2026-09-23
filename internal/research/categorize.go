package research

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/marklubin/researchguy/internal/llm"
)

// FileLocation describes where a research file should be stored.
type FileLocation struct {
	Category string `json:"category"`
	Filename string `json:"filename"`
}

// Categorize uses an LLM to determine the best category and filename for a research topic.
func Categorize(ctx context.Context, provider llm.Provider, topic string, categories []string) (FileLocation, error) {
	catList := strings.Join(categories, ", ")
	prompt := fmt.Sprintf(`Categorize this research topic. Respond with ONLY a JSON object, no other text.

Topic: "%s"
Existing categories: [%s]

Rules:
- category: lowercase kebab-case, 1-3 words. Use an existing category if the topic fits, otherwise create a new one.
- filename: lowercase kebab-case, 2-5 words. Short and descriptive. No dates unless time-specific.

{"category": "...", "filename": "..."}`, topic, catList)

	resp, err := provider.Complete(ctx, llm.Request{
		UserPrompt: prompt,
		MaxTokens:  200,
	})
	if err != nil {
		return FileLocation{}, fmt.Errorf("categorize LLM call: %w", err)
	}

	loc, err := parseFileLocation(resp)
	if err != nil {
		return FileLocation{
			Category: "uncategorized",
			Filename: Slugify(topic),
		}, nil
	}

	return loc, nil
}

// parseFileLocation extracts a FileLocation JSON from an LLM response,
// handling markdown fencing and surrounding text.
func parseFileLocation(resp string) (FileLocation, error) {
	// Try to extract JSON object from response
	re := regexp.MustCompile(`\{[^}]+\}`)
	match := re.FindString(resp)
	if match == "" {
		return FileLocation{}, fmt.Errorf("no JSON object found in response")
	}

	var loc FileLocation
	if err := json.Unmarshal([]byte(match), &loc); err != nil {
		return FileLocation{}, fmt.Errorf("parsing JSON: %w", err)
	}

	loc.Category = Slugify(loc.Category)
	loc.Filename = Slugify(loc.Filename)

	if loc.Category == "" || loc.Category == "unnamed" {
		return FileLocation{}, fmt.Errorf("empty category")
	}
	if loc.Filename == "" || loc.Filename == "unnamed" {
		return FileLocation{}, fmt.Errorf("empty filename")
	}

	return loc, nil
}

// ExistingCategories returns the names of top-level directories in the research dir.
func ExistingCategories(researchDir string) ([]string, error) {
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
	return cats, nil
}

// UniqueFilename returns base if base.md doesn't exist in dir,
// otherwise returns base-2, base-3, etc. (without .md extension).
func UniqueFilename(dir, base string) string {
	if _, err := os.Stat(filepath.Join(dir, base+".md")); os.IsNotExist(err) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, err := os.Stat(filepath.Join(dir, candidate+".md")); os.IsNotExist(err) {
			return candidate
		}
	}
}
