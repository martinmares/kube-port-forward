package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestExamplesCommandShowsGuideWithoutClusterArguments(t *testing.T) {
	var output bytes.Buffer
	cmd := newExamplesCommand()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--color", "never"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	guide := output.String()
	for _, want := range []string{
		"KUBE-PORT-FORWARD EXAMPLES",
		"## FORWARD PODS AND SERVICES",
		"kube-port-forward -n demo pod/",
		"kube-port-forward -n demo 'svc/*' 10000:*",
		"--diagnostic-config diagnostic.yaml",
		"FILE · diagnostic.yaml (YAML)",
		"defaultAction: deny",
		"mode HTTPS",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("examples output is missing %q", want)
		}
	}
	if strings.Contains(guide, "\x1b[") {
		t.Fatal("--color never produced ANSI escapes")
	}
}

func TestExamplesCommandColorAndArgumentValidation(t *testing.T) {
	for _, args := range [][]string{{"--color", "invalid"}, {"unexpected"}} {
		cmd := newExamplesCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("examples accepted %q", args)
		}
	}

	var output bytes.Buffer
	cmd := newExamplesCommand()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--color", "always"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\x1b[1;36mKUBE-PORT-FORWARD EXAMPLES\x1b[0m") {
		t.Fatal("--color always did not color the heading")
	}
}
