package db

import (
	"strings"
	"testing"
)

func TestToMigrateURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"postgres scheme with sslmode",
			"postgres://lemmyscheduler@127.0.0.1:5433/lemmyscheduler?sslmode=disable",
			"pgx5://lemmyscheduler@127.0.0.1:5433/lemmyscheduler?sslmode=disable",
		},
		{
			"postgresql scheme",
			"postgresql://u:p@db.example/app?sslmode=require&connect_timeout=3",
			"pgx5://u:p@db.example/app?sslmode=require&connect_timeout=3",
		},
		{
			"no query",
			"postgres://localhost/app",
			"pgx5://localhost/app",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toMigrateURL(tt.in)
			if err != nil {
				t.Fatalf("toMigrateURL(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("toMigrateURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToMigrateURLRejectsUnexpectedSchemes(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"http", "http://localhost/app"},
		{"mysql", "mysql://localhost/app"},
		{"already pgx5", "pgx5://localhost/app"},
		{"key value DSN", "host=localhost dbname=app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := toMigrateURL(tt.in); err == nil {
				t.Fatalf("toMigrateURL(%q) = %q, want error", tt.in, got)
			}
		})
	}
}

// The URL can carry a password, so no error may repeat it.
func TestToMigrateURLErrorsDoNotLeakPassword(t *testing.T) {
	tests := []string{
		"mysql://user:hunter2@localhost/app",
		"postgres://user:hunter2@local host/app", // invalid host: url.Parse fails
	}
	for _, in := range tests {
		_, err := toMigrateURL(in)
		if err == nil {
			t.Fatalf("toMigrateURL(%q) succeeded, want error", in)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("error leaks the password: %v", err)
		}
	}
}
