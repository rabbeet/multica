package githubpoll

import (
	"context"
	"errors"
	"testing"
)

func TestEnvPATSource_Token_FromEnvVar(t *testing.T) {
	s := EnvPATSource{
		EnvVar: "TEST_PAT",
		Getenv: func(name string) string {
			if name == "TEST_PAT" {
				return "ghp_abcdef"
			}
			return ""
		},
	}
	got, exp, err := s.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ghp_abcdef" {
		t.Errorf("token = %q, want %q", got, "ghp_abcdef")
	}
	if !exp.IsZero() {
		t.Errorf("expiresAt = %v, want zero (PAT never expires from poller's POV)", exp)
	}
}

func TestEnvPATSource_Token_DefaultEnvVar(t *testing.T) {
	calls := []string{}
	s := EnvPATSource{
		Getenv: func(name string) string {
			calls = append(calls, name)
			return "secret"
		},
	}
	if _, _, err := s.Token(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "MULTICA_GITHUB_API_TOKEN" {
		t.Errorf("expected lookup of MULTICA_GITHUB_API_TOKEN, got %v", calls)
	}
}

func TestEnvPATSource_Token_Empty(t *testing.T) {
	s := EnvPATSource{EnvVar: "TEST_PAT", Getenv: func(string) string { return "" }}
	_, _, err := s.Token(context.Background())
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("err = %v, want ErrNoToken", err)
	}
}

func TestEnvPATSource_Token_Whitespace(t *testing.T) {
	s := EnvPATSource{EnvVar: "TEST_PAT", Getenv: func(string) string { return "   \n\t  " }}
	_, _, err := s.Token(context.Background())
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("whitespace-only token should report ErrNoToken, got %v", err)
	}
}

func TestEnvPATSource_Token_TrimsSpaces(t *testing.T) {
	s := EnvPATSource{EnvVar: "TEST_PAT", Getenv: func(string) string { return "  ghp_x  \n" }}
	tok, _, err := s.Token(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if tok != "ghp_x" {
		t.Errorf("token = %q, want %q (trimmed)", tok, "ghp_x")
	}
}

func TestEnvPATSource_Token_PicksUpRotation(t *testing.T) {
	// Operator rotates the secret in-place; next call sees the new
	// value without restart. Models PUL-141's expected behavior for
	// the App installation source.
	current := "old"
	s := EnvPATSource{EnvVar: "TEST_PAT", Getenv: func(string) string { return current }}
	tok, _, _ := s.Token(context.Background())
	if tok != "old" {
		t.Fatalf("token = %q, want %q", tok, "old")
	}
	current = "new"
	tok, _, _ = s.Token(context.Background())
	if tok != "new" {
		t.Errorf("token = %q after rotation, want %q", tok, "new")
	}
}

func TestStaticTokenSource(t *testing.T) {
	s := NewStaticTokenSource("xyz")
	got, _, err := s.Token(context.Background())
	if err != nil || got != "xyz" {
		t.Errorf("Token() = (%q, %v), want (%q, nil)", got, err, "xyz")
	}

	empty := NewStaticTokenSource("")
	if _, _, err := empty.Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Errorf("empty static source err = %v, want ErrNoToken", err)
	}
}
