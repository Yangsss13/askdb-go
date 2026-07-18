// Package datasource implements data-source management: CRUD, credential
// encryption, connection testing, and the connection-factory used by the Worker.
package datasource

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/Yangsss13/askdb-go/internal/crypto"
	"github.com/Yangsss13/askdb-go/internal/netguard"
)

const (
	maxLabelLen    = 100
	maxHostLen     = 253
	maxDBNameLen   = 64
	maxUsernameLen = 64
	maxPasswordLen = 128

	defaultConnectTimeoutSec = 5
)

// Encrypter encrypts and decrypts passwords. Implemented by crypto.Cipher.
type Encrypter interface {
	Encrypt(plaintext, aad string) (string, error)
	Decrypt(ciphertext, aad string) (string, error)
}

// Service handles data-source CRUD, credential encryption, connection testing,
// and building per-job database connections for the Worker.
type Service struct {
	repo  Repository
	enc   Encrypter
	guard *netguard.Validator
	now   func() time.Time
}

// NewService wires the service dependencies.
func NewService(repo Repository, enc Encrypter, guard *netguard.Validator) *Service {
	return &Service{repo: repo, enc: enc, guard: guard, now: time.Now}
}

// CreateInput holds the caller-supplied fields for a new data source.
// No DSN is accepted; password is stored encrypted, never returned.
type CreateInput struct {
	Label             string
	Host              string
	Port              uint16
	DatabaseName      string
	Username          string
	Password          string // plaintext, validated but never stored as-is
	TLSMode           TLSMode
	ConnectTimeoutSec uint8 // 0 → defaultConnectTimeoutSec
}

// UpdateInput holds the optional fields for an existing data source.
// A nil pointer means "keep current value".
type UpdateInput struct {
	Label             *string
	Host              *string
	Port              *uint16
	DatabaseName      *string
	Username          *string
	Password          *string // nil = keep; empty string = error; non-empty = replace
	TLSMode           *TLSMode
	ConnectTimeoutSec *uint8
}

// Create validates input, encrypts the password, and persists the new source.
// It uses a two-step transaction: insert first (to get the stable ID needed for
// the AAD), then encrypt and update password_ciphertext within the same TX.
func (s *Service) Create(ctx context.Context, userID uint64, in CreateInput) (*DataSource, error) {
	if err := validateCreateInput(in); err != nil {
		return nil, err
	}

	timeout := in.ConnectTimeoutSec
	if timeout == 0 {
		timeout = defaultConnectTimeoutSec
	}

	now := s.now()
	var created DataSource

	txErr := s.repo.WithTx(ctx, func(tx Repository) error {
		// Step 1: Insert with an empty placeholder so the row gets its PK.
		ds := &DataSource{
			UserID:             userID,
			Label:              in.Label,
			Host:               in.Host,
			Port:               in.Port,
			DatabaseName:       in.DatabaseName,
			Username:           in.Username,
			PasswordCiphertext: "", // filled in step 2
			TLSMode:            string(in.TLSMode),
			ConnectTimeoutSec:  timeout,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if err := tx.Create(ctx, ds); err != nil {
			return err
		}

		// Step 2: Encrypt with the stable ID as AAD and write back.
		ciphertext, err := s.enc.Encrypt(in.Password, crypto.AAD(ds.ID))
		if err != nil {
			return fmt.Errorf("datasource: encrypt: %w", err)
		}
		if err := tx.UpdateCiphertext(ctx, ds.ID, ciphertext); err != nil {
			return err
		}
		ds.PasswordCiphertext = ciphertext
		created = *ds
		return nil
	})

	if txErr != nil {
		if errors.Is(txErr, ErrDuplicateLabel) {
			return nil, errDuplicateLabel()
		}
		return nil, errInternal()
	}
	return &created, nil
}

// GetByID returns the source for the owner; cross-user or missing → 404.
func (s *Service) GetByID(ctx context.Context, id, userID uint64) (*DataSource, error) {
	ds, err := s.repo.FindByID(ctx, id, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, errNotFound()
	}
	if err != nil {
		return nil, errInternal()
	}
	return ds, nil
}

// List returns all non-deleted sources owned by userID.
func (s *Service) List(ctx context.Context, userID uint64) ([]*DataSource, error) {
	sources, err := s.repo.List(ctx, userID)
	if err != nil {
		return nil, errInternal()
	}
	return sources, nil
}

// Update applies partial changes to an existing data source.
// If Password is non-nil, it must be non-empty and will be re-encrypted.
func (s *Service) Update(ctx context.Context, id, userID uint64, in UpdateInput) (*DataSource, error) {
	if err := validateUpdateInput(in); err != nil {
		return nil, err
	}

	patch := UpdatePatch{
		Label:             in.Label,
		Host:              in.Host,
		Port:              in.Port,
		DatabaseName:      in.DatabaseName,
		Username:          in.Username,
		ConnectTimeoutSec: in.ConnectTimeoutSec,
	}
	if in.TLSMode != nil {
		mode := string(*in.TLSMode)
		patch.TLSMode = &mode
	}

	var updateErr error
	if in.Password != nil {
		updateErr = s.repo.WithTx(ctx, func(tx Repository) error {
			// Re-fetch to get current ID (already known, but also validates ownership).
			ds, err := tx.FindByID(ctx, id, userID)
			if err != nil {
				return err
			}
			ciphertext, err := s.enc.Encrypt(*in.Password, crypto.AAD(ds.ID))
			if err != nil {
				return fmt.Errorf("datasource: encrypt: %w", err)
			}
			patch.PasswordCipher = &ciphertext
			return tx.Update(ctx, id, userID, patch)
		})
	} else {
		updateErr = s.repo.Update(ctx, id, userID, patch)
	}

	if updateErr != nil {
		if errors.Is(updateErr, ErrNotFound) {
			return nil, errNotFound()
		}
		if errors.Is(updateErr, ErrDuplicateLabel) {
			return nil, errDuplicateLabel()
		}
		return nil, errInternal()
	}

	ds, err := s.repo.FindByID(ctx, id, userID)
	if err != nil {
		return nil, errInternal()
	}
	return ds, nil
}

// Delete soft-deletes the data source after confirming no active jobs reference it.
// The check + delete is done inside a transaction with a SELECT FOR SHARE to
// prevent a concurrent Submit from slipping between the check and the deletion.
func (s *Service) Delete(ctx context.Context, id, userID uint64) error {
	now := s.now()
	return s.repo.WithTx(ctx, func(tx Repository) error {
		// Verify ownership; returns ErrNotFound for cross-user or already-deleted.
		if _, err := tx.FindByID(ctx, id, userID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return errNotFound()
			}
			return errInternal()
		}

		// Atomic active-job check with a shared lock.
		hasActive, err := tx.HasActiveJobs(ctx, id)
		if err != nil {
			return errInternal()
		}
		if hasActive {
			return errHasActiveJobs()
		}

		if err := tx.SoftDelete(ctx, id, userID, now); err != nil {
			if errors.Is(err, ErrNotFound) {
				return errNotFound()
			}
			return errInternal()
		}
		return nil
	})
}

// TestConnection validates the host/port, decrypts the password, opens a
// one-shot connection, pings, and closes it. It returns a safe error message
// on failure; the raw driver error is never exposed.
func (s *Service) TestConnection(ctx context.Context, id, userID uint64) error {
	ds, err := s.repo.FindByID(ctx, id, userID)
	if errors.Is(err, ErrNotFound) {
		return errNotFound()
	}
	if err != nil {
		return errInternal()
	}

	if err := s.openAndPing(ctx, ds); err != nil {
		return err
	}
	return nil
}

// OpenDB opens a validated, single-use *sql.DB for the given data source.
// Used by the Worker to run a single query; caller must Close the returned DB.
// Errors returned are safe for internal logging but must not be sent to clients.
func (s *Service) OpenDB(ctx context.Context, ds *DataSource) (*sql.DB, error) {
	password, err := s.enc.Decrypt(ds.PasswordCiphertext, crypto.AAD(ds.ID))
	if err != nil {
		return nil, fmt.Errorf("datasource: decrypt: %w", err)
	}

	target, err := s.guard.Validate(ctx, ds.Host, ds.Port)
	if err != nil {
		return nil, fmt.Errorf("datasource: net validate: %w", err)
	}

	db, err := s.buildDB(ds, password, target)
	if err != nil {
		return nil, err
	}
	return db, nil
}

// openAndPing is the shared implementation for TestConnection.
func (s *Service) openAndPing(ctx context.Context, ds *DataSource) error {
	password, err := s.enc.Decrypt(ds.PasswordCiphertext, crypto.AAD(ds.ID))
	if err != nil {
		// Decryption failure is an internal misconfiguration.
		return errInternal()
	}

	target, err := s.guard.Validate(ctx, ds.Host, ds.Port)
	if err != nil {
		return errConnectFailed("address validation failed")
	}

	db, err := s.buildDB(ds, password, target)
	if err != nil {
		return errConnectFailed("could not build connection")
	}
	defer db.Close()

	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(ds.ConnectTimeoutSec)*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return errConnectFailed("connection test failed")
	}
	return nil
}

// buildDB constructs a *sql.DB from the validated target and decrypted password.
//
// Design decisions:
//   - cfg.TLS is set per-config (not the global TLSConfig string registry).
//   - Each call registers a unique network name in RegisterDialContext so
//     concurrent calls never overwrite each other's pinned-IP dialer.
//   - The dialer closure captures pinnedAddr at Validate time; no re-resolution
//     can occur during Dial (DNS-rebinding protection).
//   - TLSDisabled is only permitted when all resolved IPs are in the allowlist.
//   - sql.OpenDB(NewConnector) is used so the Config struct is passed directly.
func (s *Service) buildDB(ds *DataSource, password string, target *netguard.ValidatedTarget) (*sql.DB, error) {
	tlsMode := TLSMode(ds.TLSMode)

	// TLS disabled requires that ALL resolved IPs are in the explicit allowlist.
	if tlsMode == TLSDisabled && !s.guard.AllInAllowlist(target.ResolvedIPs) {
		return nil, fmt.Errorf("datasource: tls_mode 'disabled' is only permitted for explicitly allowlisted private targets")
	}

	// Build per-config TLS without touching any global TLS registry.
	var tlsCfg *tls.Config
	switch tlsMode {
	case TLSVerifyFull:
		tlsCfg = &tls.Config{
			ServerName: target.Host, // original hostname for SNI + cert CN check
			MinVersion: tls.VersionTLS12,
		}
		if sysCAs, err := x509.SystemCertPool(); err == nil {
			tlsCfg.RootCAs = sysCAs
		}
	case TLSDisabled:
		// tlsCfg stays nil → driver uses no TLS.
	default:
		return nil, fmt.Errorf("datasource: unsupported tls_mode %q", ds.TLSMode)
	}

	// Pin the connection to the first validated IP. The dialer closure captures
	// pinnedAddr at Validate time; no re-resolution can occur during Dial.
	pinnedIP := target.ResolvedIPs[0].String()
	pinnedAddr := net.JoinHostPort(pinnedIP, fmt.Sprintf("%d", ds.Port))
	pinDialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", pinnedAddr)
	}

	// Use a unique network name per buildDB call so concurrent invocations
	// never overwrite each other's dialer in the global registry.
	rawKey := make([]byte, 8)
	if _, err := rand.Read(rawKey); err != nil {
		return nil, fmt.Errorf("datasource: generate dialer key: %w", err)
	}
	dialNetName := "askdb-pinned-" + hex.EncodeToString(rawKey)
	drivermysql.RegisterDialContext(dialNetName, pinDialer)

	cfg := drivermysql.Config{
		User:                 ds.Username,
		Passwd:               password,
		Net:                  dialNetName, // unique per call → no dialer collision
		Addr:                 net.JoinHostPort(target.Host, fmt.Sprintf("%d", ds.Port)),
		DBName:               ds.DatabaseName,
		ParseTime:            true,
		AllowNativePasswords: true,
		Timeout:              time.Duration(ds.ConnectTimeoutSec) * time.Second,
		TLS:                  tlsCfg, // per-config, not global registry
	}

	// Use NewConnector + sql.OpenDB so the Config is consumed directly without
	// DSN string serialization/parsing round-trip.
	connector, err := drivermysql.NewConnector(&cfg)
	if err != nil {
		return nil, fmt.Errorf("datasource: new connector: %w", err)
	}
	sqlDB := sql.OpenDB(connector)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(0)
	sqlDB.SetConnMaxLifetime(0)
	return sqlDB, nil
}

// --- input validation helpers ---

func validateCreateInput(in CreateInput) *ServiceError {
	if err := validateLabel(in.Label); err != nil {
		return err
	}
	if err := validateHost(in.Host); err != nil {
		return err
	}
	if in.Port == 0 {
		return errBadInput("port must be non-zero")
	}
	if err := validateDBName(in.DatabaseName); err != nil {
		return err
	}
	if err := validateUsername(in.Username); err != nil {
		return err
	}
	if in.Password == "" {
		return errBadInput("password must not be empty")
	}
	if utf8.RuneCountInString(in.Password) > maxPasswordLen {
		return errBadInput("password too long")
	}
	if err := validateTLSMode(in.TLSMode); err != nil {
		return err
	}
	return nil
}

func validateUpdateInput(in UpdateInput) *ServiceError {
	if in.Label != nil {
		if err := validateLabel(*in.Label); err != nil {
			return err
		}
	}
	if in.Host != nil {
		if err := validateHost(*in.Host); err != nil {
			return err
		}
	}
	if in.Port != nil && *in.Port == 0 {
		return errBadInput("port must be non-zero")
	}
	if in.DatabaseName != nil {
		if err := validateDBName(*in.DatabaseName); err != nil {
			return err
		}
	}
	if in.Username != nil {
		if err := validateUsername(*in.Username); err != nil {
			return err
		}
	}
	if in.Password != nil {
		if *in.Password == "" {
			return errBadInput("password must not be empty when provided")
		}
		if utf8.RuneCountInString(*in.Password) > maxPasswordLen {
			return errBadInput("password too long")
		}
	}
	if in.TLSMode != nil {
		if err := validateTLSMode(*in.TLSMode); err != nil {
			return err
		}
	}
	return nil
}

func validateLabel(s string) *ServiceError {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxLabelLen {
		return errBadInput("label must be 1–100 characters")
	}
	return nil
}

func validateHost(s string) *ServiceError {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxHostLen {
		return errBadInput("host must be 1–253 characters")
	}
	for _, ch := range []string{"@", "/", "?", "#", " "} {
		if strings.Contains(s, ch) {
			return errBadInput("host contains invalid characters")
		}
	}
	return nil
}

func validateDBName(s string) *ServiceError {
	if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > maxDBNameLen {
		return errBadInput("database_name must be 1–64 characters")
	}
	return nil
}

func validateUsername(s string) *ServiceError {
	if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > maxUsernameLen {
		return errBadInput("username must be 1–64 characters")
	}
	return nil
}

func validateTLSMode(m TLSMode) *ServiceError {
	switch m {
	case TLSDisabled, TLSVerifyFull:
		return nil
	default:
		return &ServiceError{
			Code:    ErrCodeTLSNotPermitted,
			Message: "tls_mode must be 'disabled' or 'verify-full'",
			Status:  400,
		}
	}
}

// GetByIDRaw loads a data source by ID without ownership or soft-delete checks.
// Used internally by the Worker so it can still execute jobs even if the source
// was soft-deleted after the job was queued.
func (s *Service) GetByIDRaw(ctx context.Context, id uint64) (*DataSource, error) {
	return s.repo.FindByIDRaw(ctx, id)
}
