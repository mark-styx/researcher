package research

import (
	"regexp"
	"strings"
)

// Slugify converts a topic string to a filesystem-safe directory name.
func Slugify(topic string) string {
	s := strings.ToLower(strings.TrimSpace(topic))
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "unnamed"
	}
	return s
}
