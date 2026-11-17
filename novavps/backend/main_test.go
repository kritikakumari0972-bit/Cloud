package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestAdminPasswordEnvironmentOverridesSavedHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin_password.hash")
	oldHash, err := bcrypt.GenerateFromPassword([]byte("previous-test-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, oldHash, 0600); err != nil {
		t.Fatal(err)
	}

	const configuredPassword = "new-test-password-123"
	t.Setenv("NOVAVPS_ADMIN_PASSWORD", configuredPassword)

	plain, hash, created, err := loadOrCreateAdminCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "" {
		t.Fatal("configured password must not be returned for logging")
	}
	if created {
		t.Fatal("environment-provided password must not be treated as a generated password")
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(configuredPassword)); err != nil {
		t.Fatal("returned hash does not match configured password")
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte("previous-test-password")); err == nil {
		t.Fatal("previous password still matches after rotation")
	}

	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(stored, []byte(configuredPassword)); err != nil {
		t.Fatal("persisted hash does not match configured password")
	}
}

func TestAdminPasswordEnvironmentRejectsShortPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin_password.hash")
	t.Setenv("NOVAVPS_ADMIN_PASSWORD", "too-short")

	if _, _, _, err := loadOrCreateAdminCredentials(path); err == nil {
		t.Fatal("expected short environment password to be rejected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("short password must not create a hash file")
	}
}
