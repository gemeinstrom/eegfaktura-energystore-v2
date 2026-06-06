package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeIDToken stubs idTokenClaimsExtractor with a pre-canned PlatformClaims.
type fakeIDToken struct{ claims PlatformClaims }

func (f *fakeIDToken) Claims(target any) error {
	b, _ := json.Marshal(f.claims)
	return json.Unmarshal(b, target)
}

type fakeVerifier struct {
	wantRaw string
	tok     *fakeIDToken
	err     error
}

func (f *fakeVerifier) Verify(_ context.Context, raw string) (idTokenClaimsExtractor, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.wantRaw != "" && raw != f.wantRaw {
		return nil, errors.New("token mismatch")
	}
	return f.tok, nil
}

type fakePasswordVerifier struct {
	wantUser, wantPass string
	tok                *fakeIDToken
	err                error
}

func (f *fakePasswordVerifier) AuthenticateUserWithPassword(_ context.Context, user, pass string) (idTokenClaimsExtractor, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.wantUser != "" && (user != f.wantUser || pass != f.wantPass) {
		return nil, errors.New("creds mismatch")
	}
	return f.tok, nil
}

func basicHeader(user, pass string) string {
	return BasicSchema + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func okHandler() HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request, c *PlatformClaims, tenant string) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(tenant + ":" + strings.Join(c.Tenants, ",")))
	}
}

func TestHasTenantCaseInsensitive(t *testing.T) {
	c := &PlatformClaims{Tenants: []string{"VFEEG"}}
	if !c.HasTenant("vfeeg") || !c.HasTenant("vFeEg") {
		t.Fatal("expected case-insensitive match")
	}
	if c.HasTenant("other") {
		t.Fatal("unexpected match")
	}
}

func TestExtractBearer(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer abc.def.ghi")
	got, err := extractBearer(r)
	if err != nil || got != "abc.def.ghi" {
		t.Fatalf("bearer extract: got=%q err=%v", got, err)
	}
}

func TestExtractBasicURLEncoding(t *testing.T) {
	// v1 used URLEncoding. Confirm we still accept it.
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", BasicSchema+base64.URLEncoding.EncodeToString([]byte("u:p")))
	user, pass, err := extractBasic(r)
	if err != nil || user != "u" || pass != "p" {
		t.Fatalf("basic url-encoding extract: %q %q %v", user, pass, err)
	}
}

func TestProtectApp_OK(t *testing.T) {
	m := New(&fakeVerifier{tok: &fakeIDToken{claims: PlatformClaims{Tenants: []string{"VFEEG"}}}}, nil, Options{})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	m.ProtectApp(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "VFEEG:VFEEG" {
		t.Fatalf("body: %q", rec.Body.String())
	}
}

func TestProtectApp_MissingAuth(t *testing.T) {
	m := New(&fakeVerifier{}, nil, Options{})
	r := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	m.ProtectApp(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestProtectApp_BadSchema(t *testing.T) {
	m := New(&fakeVerifier{}, nil, Options{})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Mac something")
	rec := httptest.NewRecorder()
	m.ProtectApp(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestProtectApp_VerifyFails(t *testing.T) {
	m := New(&fakeVerifier{err: errors.New("invalid")}, nil, Options{})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer x")
	r.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	m.ProtectApp(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestProtectApp_TenantNotInClaims(t *testing.T) {
	m := New(&fakeVerifier{tok: &fakeIDToken{claims: PlatformClaims{Tenants: []string{"OTHER"}}}}, nil, Options{})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer x")
	r.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	m.ProtectApp(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestProtectAPI_OK(t *testing.T) {
	m := New(nil, &fakePasswordVerifier{
		tok: &fakeIDToken{claims: PlatformClaims{Tenants: []string{"VFEEG"}}},
	}, Options{})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", basicHeader("svc", "secret"))
	r.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	m.ProtectAPI(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProtectAPI_PasswordGrantFails(t *testing.T) {
	m := New(nil, &fakePasswordVerifier{err: errors.New("denied")}, Options{})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", basicHeader("svc", "wrong"))
	r.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	m.ProtectAPI(okHandler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestGQL_TenantViaTenantHeader(t *testing.T) {
	m := New(&fakeVerifier{tok: &fakeIDToken{claims: PlatformClaims{Tenants: []string{"VFEEG"}}}}, nil, Options{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(ForContextTenant(r.Context())))
	})
	r := httptest.NewRequest("POST", "/query", nil)
	r.Header.Set("Authorization", "Bearer x")
	r.Header.Set("tenant", "vfeeg")
	rec := httptest.NewRecorder()
	m.GQL(next).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "VFEEG" {
		t.Fatalf("expected tenant on ctx, got %q", rec.Body.String())
	}
}

func TestGQL_FallbackToXTenant(t *testing.T) {
	m := New(&fakeVerifier{tok: &fakeIDToken{claims: PlatformClaims{Tenants: []string{"VFEEG"}}}}, nil, Options{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	r := httptest.NewRequest("POST", "/query", nil)
	r.Header.Set("Authorization", "Bearer x")
	r.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	m.GQL(next).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}
