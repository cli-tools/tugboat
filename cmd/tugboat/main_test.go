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

func TestParseVerbose(t *testing.T) {
	for _, args := range [][]string{
		{"--verbose", "t1", "--workers", "2"},
		{"t1", "--workers", "2", "--verbose"},
	} {
		verbose, remaining := parseVerbose(args)
		workers, targets := parseWorkers(remaining)
		if !verbose || workers != 2 || !reflect.DeepEqual(targets, []string{"t1"}) {
			t.Fatalf("parse %v: verbose=%v workers=%d targets=%v", args, verbose, workers, targets)
		}
	}
	verbose, targets := parseVerbose([]string{"t1"})
	if verbose || !reflect.DeepEqual(targets, []string{"t1"}) {
		t.Fatalf("unexpected default: %v %v", verbose, targets)
	}
}
