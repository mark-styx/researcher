package main

import (
	"strings"
	"testing"
)

func TestMCPCmd_RejectsUnknownProfile(t *testing.T) {
	testSetup(t)
	_, _, err := runCmdStdout(t, "mcp", "--profile", "write")
	if err == nil || !strings.Contains(err.Error(), `unknown --profile "write"`) || !strings.Contains(err.Error(), "or web") {
		t.Errorf("err = %v", err)
	}
}
