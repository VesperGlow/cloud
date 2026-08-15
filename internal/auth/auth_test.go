package auth

import (
	"context"
	"testing"
	"time"

	"github.com/VesperGlow/cloud/internal/database"
)

var testParams = Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestLoginSessionAndExpiry(t *testing.T) {
	db, err := database.Open(t.TempDir() + "/cloud.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &Service{DB: db, Params: testParams}
	if err := svc.Initialize(context.Background(), "admin", "a-secure-test-password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(context.Background(), "admin", "wrong-password"); err == nil {
		t.Fatal("wrong password was accepted")
	}
	token, _, err := svc.Login(context.Background(), "admin", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Authenticate(context.Background(), token); err != nil || got != "admin" {
		t.Fatalf("authenticate = %q, %v", got, err)
	}
	if _, err := db.Exec(`UPDATE sessions SET expires_at=? WHERE token_hash=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), TokenHash(token)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), token); err == nil {
		t.Fatal("expired session was accepted")
	}
}

func TestPasswordHashIsSalted(t *testing.T) {
	a, err := HashPassword("correct horse battery staple", testParams)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("correct horse battery staple", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("password hashes must use independent salts")
	}
	if ok, err := VerifyPassword("correct horse battery staple", a); err != nil || !ok {
		t.Fatalf("verification = %v, %v", ok, err)
	}
}
