package main

import "testing"

func TestPrivateTablesAreUniqueAndExcludeRegistry(t *testing.T) {
	seen := make(map[string]bool, len(privateTables))
	for _, table := range privateTables {
		if table == "shein_shops" {
			t.Fatal("shared shop registry must not be moved")
		}
		if seen[table] {
			t.Fatalf("duplicate private table %q", table)
		}
		seen[table] = true
	}
	if len(privateTables) != 32 {
		t.Fatalf("private table count = %d, want 32", len(privateTables))
	}
}

func TestMigrationIdentifiers(t *testing.T) {
	if !shopCodePattern.MatchString("beauty-hangers-home") {
		t.Fatal("valid shop code was rejected")
	}
	if !schemaNamePattern.MatchString("shein_beauty_hangers_home") {
		t.Fatal("valid schema name was rejected")
	}
	if schemaNamePattern.MatchString("public;drop") {
		t.Fatal("unsafe schema name was accepted")
	}
}
