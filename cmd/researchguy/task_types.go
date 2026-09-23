package main

import "fmt"

var allowedTaskTypes = map[string]bool{
	"ask":     true,
	"compare": true,
	"dive":    true,
	"enrich":  true,
	"review":  true,
	"watch":   true,
}

func validateTaskType(taskType string) error {
	if allowedTaskTypes[taskType] {
		return nil
	}
	return fmt.Errorf("invalid --type %q (expected one of: ask, compare, dive, enrich, review, watch)", taskType)
}
