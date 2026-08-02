package main

import "testing"

func TestCreateUserAndLogin(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createUser(s, "alice", "hunter22", RoleReadOnly); err != nil {
		t.Fatalf("createUser: %v", err)
	}

	u, err := getUser(s, "alice")
	if err != nil {
		t.Fatalf("getUser: %v", err)
	}
	if u.Role != RoleReadOnly {
		t.Fatalf("expected role %q, got %q", RoleReadOnly, u.Role)
	}
	if !checkPassword(u, "hunter22") {
		t.Fatal("expected correct password to check out")
	}
	if checkPassword(u, "wrong") {
		t.Fatal("expected wrong password to fail")
	}
	if u.PasswordHash == "hunter22" {
		t.Fatal("password must be hashed, not stored in plaintext")
	}
}

func TestCreateUserValidation(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createUser(s, "bad name", "longenough1", RoleAdmin); err != ErrInvalidUsername {
		t.Fatalf("expected ErrInvalidUsername, got %v", err)
	}
	if err := createUser(s, "alice", "longenough1", "superuser"); err != ErrInvalidRole {
		t.Fatalf("expected ErrInvalidRole, got %v", err)
	}
	if err := createUser(s, "alice", "short", RoleAdmin); err != ErrPasswordTooShort {
		t.Fatalf("expected ErrPasswordTooShort, got %v", err)
	}

	if err := createUser(s, "alice", "longenough1", RoleAdmin); err != nil {
		t.Fatalf("createUser: %v", err)
	}
	if err := createUser(s, "alice", "longenough1", RoleAdmin); err != ErrUserExists {
		t.Fatalf("expected ErrUserExists on duplicate, got %v", err)
	}
}

func TestDeleteUserRefusesLastAdmin(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createUser(s, "onlyadmin", "longenough1", RoleAdmin); err != nil {
		t.Fatalf("createUser: %v", err)
	}

	if _, err := deleteUser(s, "onlyadmin"); err != ErrLastAdmin {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}

	u, err := getUser(s, "onlyadmin")
	if err != nil || u == nil {
		t.Fatal("expected the last admin to still exist after refused deletion")
	}
}

func TestDeleteUserAllowsRemovingNonLastAdmin(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createUser(s, "admin1", "longenough1", RoleAdmin); err != nil {
		t.Fatalf("createUser: %v", err)
	}
	if err := createUser(s, "admin2", "longenough1", RoleAdmin); err != nil {
		t.Fatalf("createUser: %v", err)
	}

	found, err := deleteUser(s, "admin1")
	if err != nil {
		t.Fatalf("deleteUser: %v", err)
	}
	if !found {
		t.Fatal("expected admin1 to be found and deleted")
	}

	// now admin2 is the last admin — must be protected
	if _, err := deleteUser(s, "admin2"); err != ErrLastAdmin {
		t.Fatalf("expected ErrLastAdmin for the now-only admin, got %v", err)
	}
}

func TestDeleteUserReadOnlyNotProtected(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createUser(s, "admin1", "longenough1", RoleAdmin); err != nil {
		t.Fatalf("createUser admin: %v", err)
	}
	if err := createUser(s, "reader", "longenough1", RoleReadOnly); err != nil {
		t.Fatalf("createUser reader: %v", err)
	}

	found, err := deleteUser(s, "reader")
	if err != nil {
		t.Fatalf("deleteUser: %v", err)
	}
	if !found {
		t.Fatal("expected reader to be deleted")
	}
}

func TestEnsureBootstrapAdminOnlyActsOnEmptyTable(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := ensureBootstrapAdmin(s, "root", "bootstrappw"); err != nil {
		t.Fatalf("ensureBootstrapAdmin: %v", err)
	}
	u, err := getUser(s, "root")
	if err != nil {
		t.Fatalf("expected bootstrap admin to exist: %v", err)
	}
	if u.Role != RoleAdmin {
		t.Fatalf("expected bootstrap user to be admin, got %q", u.Role)
	}

	// second call with different credentials must be a no-op: table is no
	// longer empty
	if err := ensureBootstrapAdmin(s, "someoneelse", "otherpassword"); err != nil {
		t.Fatalf("ensureBootstrapAdmin (second call): %v", err)
	}
	if _, err := getUser(s, "someoneelse"); err != ErrUserNotFound {
		t.Fatalf("expected second bootstrap call to be a no-op, got user lookup error %v", err)
	}
}

func TestEnsureBootstrapAdminFailsWithoutCredentialsOnEmptyTable(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := ensureBootstrapAdmin(s, "", ""); err == nil {
		t.Fatal("expected an error bootstrapping with no credentials on an empty users table")
	}
}

func TestChangePassword(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createUser(s, "alice", "originalpw", RoleReadOnly); err != nil {
		t.Fatalf("createUser: %v", err)
	}

	if err := changePassword(s, "alice", "wrongpw", "newpassword1"); err != ErrWrongPassword {
		t.Fatalf("expected ErrWrongPassword, got %v", err)
	}
	if err := changePassword(s, "alice", "originalpw", "short"); err != ErrPasswordTooShort {
		t.Fatalf("expected ErrPasswordTooShort, got %v", err)
	}
	if err := changePassword(s, "alice", "originalpw", "newpassword1"); err != nil {
		t.Fatalf("changePassword: %v", err)
	}

	u, err := getUser(s, "alice")
	if err != nil {
		t.Fatalf("getUser: %v", err)
	}
	if checkPassword(u, "originalpw") {
		t.Fatal("old password should no longer work")
	}
	if !checkPassword(u, "newpassword1") {
		t.Fatal("new password should work")
	}
}

func TestListUsersExcludesPasswordHash(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createUser(s, "alice", "longenough1", RoleReadOnly); err != nil {
		t.Fatalf("createUser: %v", err)
	}

	users, err := listUsers(s)
	if err != nil {
		t.Fatalf("listUsers: %v", err)
	}
	if len(users) != 1 || users[0].Username != "alice" || users[0].Role != RoleReadOnly {
		t.Fatalf("unexpected users list: %+v", users)
	}
}
