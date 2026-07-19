package main

import (
	"reflect"
	"testing"
)

func TestParseSyncArgs(t *testing.T) {
	removeArchived, targets := parseSyncArgs([]string{"one", "--remove-archived", "two"})
	if !removeArchived {
		t.Fatal("--remove-archived was not parsed")
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func TestParseStatusArgs(t *testing.T) {
	debug, showAll, targets := parseStatusArgs([]string{"--all", "one", "-d", "two"})
	if !debug || !showAll {
		t.Fatalf("debug = %v, showAll = %v; want both true", debug, showAll)
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}
