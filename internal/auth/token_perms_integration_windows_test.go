//go:build integration && windows

// T15C.1 — Windows DACL enforcement-tier integration test.
//
// The companion structural test `TestApplyTokenFilePerms_Windows_Structural`
// in token_perms_windows_test.go inspects the DACL shape; it does not
// verify that the OS actually denies a second local account when it
// tries to open the token file. This file does.
//
// Pattern: the enforcement check requires a second local account the
// test can log in AS. The test does not provision the account itself —
// creating a local user requires administrator rights and would make
// the default `go test` path fail on non-elevated machines. Instead
// the test reads credentials from env vars and skips if they are
// absent:
//
//   MCPGW_TEST_USER     — local account name (e.g. "mcpgwtestuser")
//   MCPGW_TEST_PASSWORD — its password (complex enough to satisfy
//                         local policy; 12+ chars with mixed classes)
//
// Operator protocol (documented in README.md §Windows DACL
// enforcement test and exercised by `make test-integration-windows`):
//
//   net user mcpgwtestuser 'Pass1234!MCPGW' /add
//   set MCPGW_TEST_USER=mcpgwtestuser
//   set MCPGW_TEST_PASSWORD=Pass1234!MCPGW
//   make test-integration-windows
//   net user mcpgwtestuser /delete
//
// Build tag `integration` keeps the test out of the default
// `go test ./...` path — the structural test (no impersonation,
// no provisioning) still runs there and covers the DACL shape.
package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// LOGON32_LOGON_INTERACTIVE and LOGON32_PROVIDER_DEFAULT mirror the
// winbase.h constants. Interactive logon type is used rather than
// BATCH so the test account does not need "Log on as a batch job"
// privilege on default Windows installs.
const (
	logon32LogonInteractive = 2
	logon32ProviderDefault  = 0
)

// advapi32!LogonUserW and advapi32!ImpersonateLoggedOnUser are not
// exposed by golang.org/x/sys/windows@v0.42, so the test loads them
// via LazyDLL. RevertToSelf IS exposed (security_windows.go:630) and
// is called directly through the x/sys wrapper.
var (
	advapi32                    = syscall.NewLazyDLL("advapi32.dll")
	procLogonUserW              = advapi32.NewProc("LogonUserW")
	procImpersonateLoggedOnUser = advapi32.NewProc("ImpersonateLoggedOnUser")
)

// logonUser calls advapi32!LogonUserW with LOGON32_LOGON_INTERACTIVE.
// Returned handle must be closed by the caller.
func logonUser(t *testing.T, user, domain, pw string) windows.Handle {
	t.Helper()
	uPtr, err := windows.UTF16PtrFromString(user)
	require.NoError(t, err, "UTF16PtrFromString(user)")
	dPtr, err := windows.UTF16PtrFromString(domain)
	require.NoError(t, err, "UTF16PtrFromString(domain)")
	pPtr, err := windows.UTF16PtrFromString(pw)
	require.NoError(t, err, "UTF16PtrFromString(password)")

	var token windows.Handle
	r1, _, e1 := procLogonUserW.Call(
		uintptr(unsafe.Pointer(uPtr)),
		uintptr(unsafe.Pointer(dPtr)),
		uintptr(unsafe.Pointer(pPtr)),
		uintptr(logon32LogonInteractive),
		uintptr(logon32ProviderDefault),
		uintptr(unsafe.Pointer(&token)),
	)
	require.NotEqualf(t, uintptr(0), r1, "LogonUserW(%q) failed: %v", user, e1)
	return token
}

// impersonateLoggedOnUser wraps advapi32!ImpersonateLoggedOnUser. The
// thread picks up the impersonation token; call RevertToSelf to go
// back to the process token.
func impersonateLoggedOnUser(t *testing.T, token windows.Handle) {
	t.Helper()
	r1, _, e1 := procImpersonateLoggedOnUser.Call(uintptr(token))
	require.NotEqualf(t, uintptr(0), r1, "ImpersonateLoggedOnUser failed: %v", e1)
}

// TestTokenPerms_Integration_Windows_DenyOtherUser is the enforcement
// counterpart to TestApplyTokenFilePerms_Windows_Structural.
//
// Setup: LoadOrCreate writes a token file with a Protected DACL that
// ALLOWs only the current user SID.
//
// Control check: current user can read the file (proves the test
// setup is not broken).
//
// Enforcement check: after LogonUser + ImpersonateLoggedOnUser as a
// second local account, os.ReadFile on the same path must fail with
// windows.ERROR_ACCESS_DENIED. Any other outcome (success, or
// different errno) is a test failure — the file perms are NOT
// protecting the token from a local attacker.
//
// This test runs ONLY under `go test -tags integration` on Windows,
// driven by `make test-integration-windows`. See file header for the
// operator protocol.
func TestTokenPerms_Integration_Windows_DenyOtherUser(t *testing.T) {
	testUser := os.Getenv("MCPGW_TEST_USER")
	testPassword := os.Getenv("MCPGW_TEST_PASSWORD")
	if testUser == "" || testPassword == "" {
		t.Skip("MCPGW_TEST_USER / MCPGW_TEST_PASSWORD not set — see README §Windows DACL enforcement test for the provisioning protocol, or run `make test-integration-windows`")
	}

	// Pin the goroutine to the current OS thread. ImpersonateLoggedOnUser
	// attaches the token to the CALLING thread; without LockOSThread the
	// Go scheduler can migrate the goroutine to a different thread
	// between the impersonate call and os.ReadFile, causing the read to
	// run un-impersonated and the test to pass or fail for the wrong
	// reason. The golang.org/x/sys/windows own tests use the same
	// pattern (syscall_windows_test.go:137).
	runtime.LockOSThread()
	// UnlockOSThread is intentionally NOT deferred here — it is paired
	// with RevertToSelf below so the thread is only released back to the
	// scheduler after successful revert. If RevertToSelf fails, the
	// goroutine exits via t.Fatalf with the thread still locked; the Go
	// runtime then terminates the locked OS thread, which causes the OS
	// to clean up the impersonation token so no foreign-SID state leaks
	// into subsequent tests.

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.token")

	// LoadOrCreate goes through writeTokenAtomic → applyTokenFilePerms,
	// so the file on disk has exactly the same DACL the production
	// daemon installs on startup.
	_, err := LoadOrCreate(path, "")
	require.NoError(t, err)

	// Control check: without impersonation, the current user (test
	// runner's account) MUST be able to read their own file. If this
	// fails, the test itself is broken — abort before drawing
	// conclusions about enforcement.
	_, err = os.ReadFile(path)
	require.NoError(t, err, "control: current user must be able to read own file")

	// Enforcement check: log in as the test user, impersonate, retry
	// the read. The read MUST fail with ERROR_ACCESS_DENIED.
	//
	// domain="." means "this machine" (local SAM), the usual
	// convention when the account is not an AD domain user.
	token := logonUser(t, testUser, ".", testPassword)
	defer windows.CloseHandle(token)

	impersonateLoggedOnUser(t, token)
	// Revert + unlock in a single defer, in that order. If RevertToSelf
	// fails, t.Fatalf triggers goroutine exit WITHOUT running
	// UnlockOSThread — the Go runtime terminates the still-locked OS
	// thread, which drops the impersonation token with it. Releasing the
	// thread back to the scheduler only on successful revert prevents
	// subsequent tests from picking up a thread that still carries the
	// test user's SID.
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			t.Fatalf("RevertToSelf: %v (locked thread will be terminated on goroutine exit)", err)
		}
		runtime.UnlockOSThread()
	}()

	_, readErr := os.ReadFile(path)
	require.Errorf(t, readErr, "impersonated read MUST fail; current user=%s, impersonated=%s", os.Getenv("USERNAME"), testUser)
	assert.Truef(t,
		errors.Is(readErr, windows.ERROR_ACCESS_DENIED),
		"impersonated read must fail with ERROR_ACCESS_DENIED, got: %v", readErr)
}
