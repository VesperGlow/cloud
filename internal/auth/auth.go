package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/VesperGlow/cloud/internal/ids"
	"golang.org/x/crypto/argon2"
)

const sessionLifetime = 30 * 24 * time.Hour

type Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

var DefaultParams = Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}

type Service struct {
	DB       *sql.DB
	Username string
	Params   Params
}

func (s *Service) Initialize(ctx context.Context, username, password string) error {
	var existing string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='admin_password_hash'`).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if username == "" || password == "" {
		return errors.New("ADMIN_USERNAME and ADMIN_PASSWORD are required on first startup")
	}
	if len(username) > 128 || len(password) < 12 || len(password) > 1024 {
		return errors.New("administrator username/password length is invalid (password minimum is 12 characters)")
	}
	hash, err := HashPassword(password, s.params())
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{"admin_username": username, "admin_password_hash": hash} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?)`, key, value, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) Login(ctx context.Context, username, password string) (string, time.Time, error) {
	var savedUser, savedHash string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='admin_username'`).Scan(&savedUser); err != nil {
		return "", time.Time{}, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='admin_password_hash'`).Scan(&savedHash); err != nil {
		return "", time.Time{}, err
	}
	valid, err := VerifyPassword(password, savedHash)
	if err != nil || subtle.ConstantTimeCompare([]byte(username), []byte(savedUser)) != 1 || !valid {
		return "", time.Time{}, errors.New("invalid credentials")
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expires := time.Now().UTC().Add(sessionLifetime)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO sessions(id, token_hash, created_at, expires_at) VALUES(?,?,?,?)`, ids.New(), TokenHash(token), time.Now().UTC().Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano))
	return token, expires, err
}

func (s *Service) Authenticate(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", errors.New("missing session")
	}
	var expiry, username string
	err := s.DB.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE token_hash=?`, TokenHash(token)).Scan(&expiry)
	if err != nil {
		return "", errors.New("invalid session")
	}
	t, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || !t.After(time.Now().UTC()) {
		s.Logout(ctx, token)
		return "", errors.New("expired session")
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='admin_username'`).Scan(&username); err != nil {
		return "", err
	}
	return username, nil
}

func (s *Service) Logout(ctx context.Context, token string) {
	if token != "" {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, TokenHash(token))
	}
}
func (s *Service) Cleanup(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339Nano))
}
func (s *Service) params() Params {
	if s.Params.Memory == 0 {
		return DefaultParams
	}
	return s.Params
}
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func HashPassword(password string, p Params) (string, error) {
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Iterations, p.Parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("invalid password hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, errors.New("invalid argon2 parameters")
	}
	m, err := strconv.ParseUint(strings.TrimPrefix(params[0], "m="), 10, 32)
	if err != nil {
		return false, err
	}
	t, err := strconv.ParseUint(strings.TrimPrefix(params[1], "t="), 10, 32)
	if err != nil {
		return false, err
	}
	p, err := strconv.ParseUint(strings.TrimPrefix(params[2], "p="), 10, 8)
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
