package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const usersSchema = `CREATE TABLE IF NOT EXISTS users (
	username TEXT PRIMARY KEY,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`

const (
	RoleAdmin    = "admin"
	RoleReadOnly = "read-only"
)

const minPasswordLength = 8

var (
	ErrUserExists       = errors.New("user already exists")
	ErrUserNotFound     = errors.New("user not found")
	ErrInvalidRole      = fmt.Errorf("role must be %q or %q", RoleAdmin, RoleReadOnly)
	ErrInvalidUsername  = fmt.Errorf("username must match %s", collectionNameRe.String())
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", minPasswordLength)
	ErrLastAdmin        = errors.New("cannot remove the last admin user")
)

// User is a row from the users table, including the password hash — never
// serialized to JSON, never returned by any HTTP handler directly.
type User struct {
	Username     string
	PasswordHash string
	Role         string
}

// UserInfo is what gets exposed over HTTP: no password hash, ever.
type UserInfo struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

func validRole(role string) bool {
	return role == RoleAdmin || role == RoleReadOnly
}

// createUser hashes password with bcrypt and inserts a new user row.
// Reuses collectionNameRe for username validation — same safe-identifier
// shape, and critically excludes ':', which the token format
// (username:role:expiry) uses as a field separator.
func createUser(s *VecStore, username, password, role string) error {
	if !collectionNameRe.MatchString(username) {
		return ErrInvalidUsername
	}
	if !validRole(role) {
		return ErrInvalidRole
	}
	if len(password) < minPasswordLength {
		return ErrPasswordTooShort
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	if _, err := s.write.Exec(
		`INSERT INTO users(username, password_hash, role) VALUES (?, ?, ?)`,
		username, string(hash), role,
	); err != nil {
		if isUniqueConstraintErr(err) {
			return ErrUserExists
		}
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// getUser looks up a user by username, for login. Returns ErrUserNotFound
// if there's no such user.
func getUser(s *VecStore, username string) (*User, error) {
	var u User
	err := s.read.QueryRow(
		`SELECT username, password_hash, role FROM users WHERE username = ?`, username,
	).Scan(&u.Username, &u.PasswordHash, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("looking up user: %w", err)
	}
	return &u, nil
}

// checkPassword reports whether password matches the user's stored hash.
func checkPassword(u *User, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

// listUsers returns all users (no password hashes), ordered by username.
func listUsers(s *VecStore) ([]UserInfo, error) {
	rows, err := s.read.Query(`SELECT username, role FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	users := []UserInfo{}
	for rows.Next() {
		var u UserInfo
		if err := rows.Scan(&u.Username, &u.Role); err != nil {
			return nil, fmt.Errorf("scanning user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func countAdmins(s *VecStore) (int, error) {
	var n int
	if err := s.read.QueryRow(`SELECT count(*) FROM users WHERE role = ?`, RoleAdmin).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting admins: %w", err)
	}
	return n, nil
}

// deleteUser removes a user. Refuses to delete the last remaining admin —
// there's no other recovery path if that happens (no root/superuser
// bypass), so this would otherwise permanently lock everyone out.
func deleteUser(s *VecStore, username string) (bool, error) {
	u, err := getUser(s, username)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return false, nil
		}
		return false, err
	}

	if u.Role == RoleAdmin {
		n, err := countAdmins(s)
		if err != nil {
			return false, err
		}
		if n <= 1 {
			return false, ErrLastAdmin
		}
	}

	res, err := s.write.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return false, fmt.Errorf("deleting user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking rows affected: %w", err)
	}
	return n > 0, nil
}

// countUsers reports how many users exist, for bootstrap decisions.
func countUsers(s *VecStore) (int, error) {
	var n int
	if err := s.read.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting users: %w", err)
	}
	return n, nil
}

// ensureBootstrapAdmin seeds the very first admin user from env-provided
// credentials, but only if the users table is completely empty — on every
// later startup this is a no-op regardless of what the env vars contain,
// so it can't silently reset a password someone changed via the API. If
// the table is empty and no bootstrap credentials were given, returns an
// error: there would be no way to log in at all otherwise.
func ensureBootstrapAdmin(s *VecStore, username, password string) error {
	n, err := countUsers(s)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if username == "" || password == "" {
		return errors.New("no users exist yet and AUTH_USERNAME/AUTH_PASSWORD are not set — can't bootstrap the first admin")
	}
	return createUser(s, username, password, RoleAdmin)
}
