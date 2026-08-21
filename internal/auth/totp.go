package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/argon2"
)

const (
	totpConfigKey     = "admin_totp_config"
	totpRecoveryKey   = "admin_totp_recovery_codes"
	totpLastStepKey   = "admin_totp_last_step"
	totpPendingKey    = "admin_totp_pending"
	totpIssuer        = "revaro"
	totpPeriod        = 30
	setupLifetime     = 10 * time.Minute
	recoveryCount     = 10
	maxKDFMemory      = 1024 * 1024
	maxKDFIterations  = 10
	maxKDFParallelism = 16
)

var (
	ErrTOTPRequired        = errors.New("two-factor authentication required")
	ErrInvalidSecondFactor = errors.New("invalid two-factor code")
	ErrTOTPAlreadyEnabled  = errors.New("two-factor authentication already enabled")
	ErrTOTPNotEnabled      = errors.New("two-factor authentication is not enabled")
	ErrTOTPSetupExpired    = errors.New("two-factor setup expired")
)

type TOTPSetup struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

type TOTPStatus struct {
	Enabled       bool `json:"enabled"`
	RecoveryCodes int  `json:"recovery_codes"`
}

type encryptedSecret struct {
	Version     int    `json:"version"`
	Salt        string `json:"salt"`
	Nonce       string `json:"nonce"`
	Ciphertext  string `json:"ciphertext"`
	Memory      uint32 `json:"memory"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
}

type pendingTOTP struct {
	Secret    encryptedSecret `json:"secret"`
	URI       string          `json:"uri"`
	ExpiresAt string          `json:"expires_at"`
}

func (s *Service) TOTPStatus(ctx context.Context) (TOTPStatus, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, totpConfigKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return TOTPStatus{}, nil
	}
	if err != nil {
		return TOTPStatus{}, err
	}
	var encrypted encryptedSecret
	if err := json.Unmarshal([]byte(raw), &encrypted); err != nil || encrypted.Version != 1 {
		return TOTPStatus{}, errors.New("stored TOTP configuration is invalid")
	}
	status := TOTPStatus{Enabled: true}
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, totpRecoveryKey).Scan(&raw); err == nil {
		var hashes []string
		if err := json.Unmarshal([]byte(raw), &hashes); err != nil {
			return TOTPStatus{}, errors.New("stored recovery codes are invalid")
		}
		status.RecoveryCodes = len(hashes)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TOTPStatus{}, err
	}
	return status, nil
}

func (s *Service) BeginTOTPSetup(ctx context.Context, username, password string) (TOTPSetup, error) {
	if err := s.verifyCredentials(ctx, username, password); err != nil {
		return TOTPSetup{}, err
	}
	status, err := s.TOTPStatus(ctx)
	if err != nil {
		return TOTPSetup{}, err
	}
	if status.Enabled {
		return TOTPSetup{}, ErrTOTPAlreadyEnabled
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: username,
		Period:      totpPeriod,
		SecretSize:  20,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return TOTPSetup{}, err
	}
	encrypted, err := encryptSecret(password, key.Secret(), s.params())
	if err != nil {
		return TOTPSetup{}, err
	}
	pending := pendingTOTP{Secret: encrypted, URI: key.URL(), ExpiresAt: s.now().Add(setupLifetime).Format(time.RFC3339Nano)}
	raw, err := json.Marshal(pending)
	if err != nil {
		return TOTPSetup{}, err
	}
	now := s.now().Format(time.RFC3339Nano)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, totpPendingKey, string(raw), now); err != nil {
		return TOTPSetup{}, err
	}
	return TOTPSetup{Secret: key.Secret(), URI: key.URL()}, nil
}

func (s *Service) ConfirmTOTPSetup(ctx context.Context, username, password, code, sessionToken string) ([]string, error) {
	if err := s.verifyCredentials(ctx, username, password); err != nil {
		return nil, err
	}
	status, err := s.TOTPStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.Enabled {
		return nil, ErrTOTPAlreadyEnabled
	}
	var raw string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, totpPendingKey).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTOTPSetupExpired
		}
		return nil, err
	}
	var pending pendingTOTP
	if err := json.Unmarshal([]byte(raw), &pending); err != nil {
		return nil, errors.New("stored pending TOTP setup is invalid")
	}
	expires, err := time.Parse(time.RFC3339Nano, pending.ExpiresAt)
	if err != nil || !expires.After(s.now()) {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, totpPendingKey)
		return nil, ErrTOTPSetupExpired
	}
	secret, err := decryptSecret(password, pending.Secret)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	step, ok, err := validateTOTP(code, secret, s.now())
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidSecondFactor
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	configRaw, err := json.Marshal(pending.Secret)
	if err != nil {
		return nil, err
	}
	hashesRaw, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	now := s.now().Format(time.RFC3339Nano)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{
		totpConfigKey:   string(configRaw),
		totpRecoveryKey: string(hashesRaw),
		totpLastStepKey: strconv.FormatUint(step, 10),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, totpPendingKey); err != nil {
		return nil, err
	}
	if err := revokeOtherSessions(ctx, tx, sessionToken); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Service) RegenerateRecoveryCodes(ctx context.Context, username, password, code string) ([]string, error) {
	if err := s.verifyCredentials(ctx, username, password); err != nil {
		return nil, err
	}
	if err := s.consumeSecondFactor(ctx, password, code, s.now()); err != nil {
		return nil, err
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	now := s.now().Format(time.RFC3339Nano)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, totpRecoveryKey, string(raw), now); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Service) DisableTOTP(ctx context.Context, username, password, code, sessionToken string) error {
	if err := s.verifyCredentials(ctx, username, password); err != nil {
		return err
	}
	status, err := s.TOTPStatus(ctx)
	if err != nil {
		return err
	}
	if !status.Enabled {
		return ErrTOTPNotEnabled
	}
	if err := s.consumeSecondFactor(ctx, password, code, s.now()); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key IN (?,?,?,?)`, totpConfigKey, totpRecoveryKey, totpLastStepKey, totpPendingKey); err != nil {
		return err
	}
	if err := revokeOtherSessions(ctx, tx, sessionToken); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) consumeSecondFactor(ctx context.Context, password, code string, now time.Time) error {
	var raw string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, totpConfigKey).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTOTPNotEnabled
		}
		return err
	}
	var encrypted encryptedSecret
	if err := json.Unmarshal([]byte(raw), &encrypted); err != nil {
		return errors.New("stored TOTP configuration is invalid")
	}
	secret, err := decryptSecret(password, encrypted)
	if err != nil {
		return ErrInvalidCredentials
	}
	trimmed := strings.TrimSpace(code)
	if isSixDigits(trimmed) {
		step, ok, err := validateTOTP(trimmed, secret, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrInvalidSecondFactor
		}
		return s.consumeTOTPStep(ctx, step)
	}
	return s.consumeRecoveryCode(ctx, trimmed)
}

func (s *Service) consumeTOTPStep(ctx context.Context, step uint64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, totpLastStepKey).Scan(&raw)
	if err == nil {
		last, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil {
			return errors.New("stored TOTP replay state is invalid")
		}
		if step <= last {
			return ErrInvalidSecondFactor
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := s.now().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, totpLastStepKey, strconv.FormatUint(step, 10), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) consumeRecoveryCode(ctx context.Context, code string) error {
	normalized := normalizeRecoveryCode(code)
	if len(normalized) != 16 {
		return ErrInvalidSecondFactor
	}
	want := recoveryCodeHash(normalized)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, totpRecoveryKey).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidSecondFactor
		}
		return err
	}
	var hashes []string
	if err := json.Unmarshal([]byte(raw), &hashes); err != nil {
		return errors.New("stored recovery codes are invalid")
	}
	found := -1
	for i, hash := range hashes {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(want)) == 1 {
			found = i
		}
	}
	if found < 0 {
		return ErrInvalidSecondFactor
	}
	hashes = append(hashes[:found], hashes[found+1:]...)
	next, err := json.Marshal(hashes)
	if err != nil {
		return err
	}
	now := s.now().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE settings SET value=?,updated_at=? WHERE key=?`, string(next), now, totpRecoveryKey); err != nil {
		return err
	}
	return tx.Commit()
}

func validateTOTP(code, secret string, now time.Time) (uint64, bool, error) {
	if !isSixDigits(code) {
		return 0, false, nil
	}
	current := now.Unix() / totpPeriod
	for _, offset := range []int64{0, -1, 1} {
		counter := current + offset
		if counter < 0 {
			continue
		}
		candidate, err := totp.GenerateCode(secret, time.Unix(counter*totpPeriod, 0).UTC())
		if err != nil {
			return 0, false, err
		}
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return uint64(counter), true, nil
		}
	}
	return 0, false, nil
}

func isSixDigits(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func generateRecoveryCodes() ([]string, []string, error) {
	encoder := base32.StdEncoding.WithPadding(base32.NoPadding)
	codes := make([]string, 0, recoveryCount)
	hashes := make([]string, 0, recoveryCount)
	for range recoveryCount {
		data := make([]byte, 10)
		if _, err := io.ReadFull(rand.Reader, data); err != nil {
			return nil, nil, err
		}
		raw := encoder.EncodeToString(data)
		code := raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16]
		codes = append(codes, code)
		hashes = append(hashes, recoveryCodeHash(raw))
	}
	return codes, hashes, nil
}

func normalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

func recoveryCodeHash(code string) string {
	sum := sha256.Sum256([]byte("revaro-recovery-v1:" + normalizeRecoveryCode(code)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encryptSecret(password, secret string, params Params) (encryptedSecret, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return encryptedSecret{}, err
	}
	key := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return encryptedSecret{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return encryptedSecret{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return encryptedSecret{}, err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(secret), []byte("revaro-totp-v1"))
	return encryptedSecret{
		Version:     1,
		Salt:        base64.RawStdEncoding.EncodeToString(salt),
		Nonce:       base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext:  base64.RawStdEncoding.EncodeToString(ciphertext),
		Memory:      params.Memory,
		Iterations:  params.Iterations,
		Parallelism: params.Parallelism,
	}, nil
}

func decryptSecret(password string, encrypted encryptedSecret) (string, error) {
	if encrypted.Version != 1 {
		return "", errors.New("unsupported encrypted secret version")
	}
	if encrypted.Memory == 0 || encrypted.Memory > maxKDFMemory ||
		encrypted.Iterations == 0 || encrypted.Iterations > maxKDFIterations ||
		encrypted.Parallelism == 0 || encrypted.Parallelism > maxKDFParallelism {
		return "", errors.New("invalid encrypted secret KDF parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(encrypted.Salt)
	if err != nil || len(salt) != 16 {
		return "", errors.New("invalid encrypted secret salt")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(encrypted.Nonce)
	if err != nil {
		return "", errors.New("invalid encrypted secret nonce")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(encrypted.Ciphertext)
	if err != nil {
		return "", errors.New("invalid encrypted secret ciphertext")
	}
	key := argon2.IDKey([]byte(password), salt, encrypted.Iterations, encrypted.Memory, encrypted.Parallelism, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() {
		return "", errors.New("invalid encrypted secret nonce size")
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte("revaro-totp-v1"))
	if err != nil {
		return "", errors.New("could not decrypt TOTP secret")
	}
	return string(plaintext), nil
}

func revokeOtherSessions(ctx context.Context, tx *sql.Tx, sessionToken string) error {
	if sessionToken == "" {
		return errors.New("missing current session")
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash<>?`, TokenHash(sessionToken))
	return err
}
