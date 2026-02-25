package search

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/marklubin/researcher/internal/config"
)

// Query runs a grepai search against the research directory.
func Query(cfg *config.Config, query string, limit int) (string, error) {
	researchDir := config.ExpandPath(cfg.ResearchDir)

	args := []string{"search", query, "--limit", strconv.Itoa(limit)}
	cmd := exec.Command(cfg.Grepai.Binary, args...)
	cmd.Dir = researchDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("grepai search failed: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}
