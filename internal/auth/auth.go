// Package auth handles the setup token, GitHub sign-in, sessions, roles and
// the origin check (§17, SI-11, SI-14).
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/github"
)

const (
	SessionCookie = "sayso_session"
	stateCookie   = "sayso_oauth_state"
	setupCookie   = "sayso_setup"
	IdleTimeout   = 12 * time.Hour
	MaxLifetime   = 7 * 24 * time.Hour
	RoleRecheck   = 15 * time.Minute
)

// Config is what the auth service needs from the process configuration.
type Config struct {
	BaseURL         string
	SetupToken      string
	BootstrapAdmins []string
	// WebhookURL is SAYSO_PUBLIC_WEBHOOK_URL for the App manifest.
	WebhookURL string
	// SigningKey signs the setup cookie. Derive it from the secret key.
	SigningKey []byte
}

// Service is the auth feature.
type Service struct {
	pool    *pgxpool.Pool
	cfg     Config
	secrets Secrets
	GitHub  func(ctx context.Context) (*github.Client, error)
	Now     func() time.Time
	log     *slog.Logger
}

// Secrets reads and writes stored secret values.
type Secrets interface {
	github.SecretReader
	github.SecretWriter
}

func NewService(pool *pgxpool.Pool, cfg Config, sec Secrets, gh func(context.Context) (*github.Client, error), log *slog.Logger) *Service {
	return &Service{pool: pool, cfg: cfg, secrets: sec, GitHub: gh, Now: time.Now, log: log}
}

func (s *Service) secure() bool { return strings.HasPrefix(s.cfg.BaseURL, "https://") }

func (s *Service) cookie(name, value string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secure()}
	if maxAge < 0 {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(maxAge.Seconds())
	}
	return c
}

func randomToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(tok))
	return tok, sum[:], nil
}

func hashToken(tok string) []byte {
	sum := sha256.Sum256([]byte(tok))
	return sum[:]
}

// Login starts the GitHub web flow.
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	app, err := github.LoadApp(r.Context(), s.pool, s.secrets)
	if err != nil {
		s.fail(w, r, "app_missing", err)
		return
	}
	gh, err := s.GitHub(r.Context())
	if err != nil {
		s.fail(w, r, "github", err)
		return
	}
	state, _, err := randomToken()
	if err != nil {
		s.fail(w, r, "internal", err)
		return
	}
	http.SetCookie(w, s.cookie(stateCookie, state, 10*time.Minute))
	http.Redirect(w, r, gh.AuthorizeURL(app, s.cfg.BaseURL+"/auth/github/callback", state), http.StatusFound)
}

// Callback finishes the web flow, checks membership and starts a session.
func (s *Service) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := r.Cookie(stateCookie)
	if err != nil || st.Value == "" || !hmac.Equal([]byte(st.Value), []byte(r.URL.Query().Get("state"))) {
		s.fail(w, r, "bad_state", errors.New("state mismatch"))
		return
	}
	http.SetCookie(w, s.cookie(stateCookie, "", -1))
	app, err := github.LoadApp(ctx, s.pool, s.secrets)
	if err != nil {
		s.fail(w, r, "app_missing", err)
		return
	}
	gh, err := s.GitHub(ctx)
	if err != nil {
		s.fail(w, r, "github", err)
		return
	}
	userTok, err := gh.ExchangeCode(ctx, app, r.URL.Query().Get("code"), s.cfg.BaseURL+"/auth/github/callback")
	if err != nil {
		s.fail(w, r, "github", err)
		return
	}
	ghUser, err := gh.CurrentUser(ctx, userTok)
	if err != nil {
		s.fail(w, r, "github", err)
		return
	}
	if s.isBootstrapAdmin(ghUser.Login) {
		if err := s.ensureBootstrapGrant(ctx, ghUser.Login); err != nil {
			s.fail(w, r, "internal", err)
			return
		}
	}
	res, err := s.Resolve(ctx, app, ghUser.Login)
	if err != nil {
		s.fail(w, r, "github", err)
		return
	}
	if !res.Member {
		_ = audit.Write(ctx, s.pool, audit.Actor{Kind: "github", Login: ghUser.Login}, "auth.signin_refused", ghUser.Login, map[string]any{"reason": "not_member"})
		s.fail(w, r, "not_member", nil)
		return
	}
	now := s.Now()
	id, err := uuid.NewV7()
	if err != nil {
		s.fail(w, r, "internal", err)
		return
	}
	u, err := New(s.pool).UpsertUser(ctx, UpsertUserParams{
		ID: id, GithubID: ghUser.ID, Login: ghUser.Login, Name: ghUser.Name,
		Role: string(res.Role), RoleSource: res.Source, LastLoginAt: &now,
	})
	if err != nil {
		s.fail(w, r, "internal", err)
		return
	}
	tok, hash, err := randomToken()
	if err != nil {
		s.fail(w, r, "internal", err)
		return
	}
	sid, err := uuid.NewV7()
	if err != nil {
		s.fail(w, r, "internal", err)
		return
	}
	if err := New(s.pool).InsertSession(ctx, InsertSessionParams{
		ID: sid, UserID: u.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(MaxLifetime),
		Ip: clientIP(r), UserAgent: r.UserAgent(),
	}); err != nil {
		s.fail(w, r, "internal", err)
		return
	}
	http.SetCookie(w, s.cookie(SessionCookie, tok, MaxLifetime))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Service) ensureBootstrapGrant(ctx context.Context, login string) error {
	_, err := New(s.pool).GrantByLogin(ctx, login)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return s.Grant(ctx, audit.System, login, Admin)
}

// Grant gives a login a role directly (REQ-AUTH-004).
func (s *Service) Grant(ctx context.Context, by audit.Actor, login string, role Role) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := New(tx).PutGrant(ctx, PutGrantParams{Login: login, Role: string(role), GrantedBy: by.Login}); err != nil {
			return err
		}
		if u, err := New(tx).UserByLogin(ctx, login); err == nil && Role(u.Role).rank() < role.rank() {
			if _, err := New(tx).UpdateUserRole(ctx, UpdateUserRoleParams{ID: u.ID, Role: string(role), RoleSource: "grant", Status: u.Status, RolesCheckedAt: u.RolesCheckedAt}); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, by, "access.grant", login, map[string]any{"role": role})
	})
}

// Logout ends the session.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = New(s.pool).DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, s.cookie(SessionCookie, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, code string, err error) {
	if err != nil {
		s.log.Warn("sign-in failed", "code", code, "error", err)
	}
	http.Redirect(w, r, "/signin?error="+url.QueryEscape(code), http.StatusFound)
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

// User is the signed-in person of a request.
type User struct {
	ID    uuid.UUID
	Login string
	Name  string
	Role  Role
}

// Actor returns the audit actor of the user.
func (u *User) Actor() audit.Actor { return audit.User(u.ID, u.Login) }

type ctxKey int

const (
	userKey ctxKey = iota
	setupKey
)

// UserFrom returns the signed-in user, or nil.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(userKey).(*User)
	return u
}

// WithUser returns ctx with a signed-in user.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// SetupFrom reports whether the request holds a valid setup cookie.
func SetupFrom(ctx context.Context) bool {
	ok, _ := ctx.Value(setupKey).(bool)
	return ok
}

// Middleware loads the session user, applies the idle and lifetime limits,
// rechecks roles every 15 minutes, and refuses state-changing cookie
// requests from another origin.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		_, hasSession := r.Cookie(SessionCookie)
		_, hasSetup := r.Cookie(setupCookie)
		if (hasSession == nil || hasSetup == nil) && changesState(r.Method) && !s.sameOrigin(r) {
			http.Error(w, "origin check failed", http.StatusForbidden)
			return
		}
		if c, err := r.Cookie(SessionCookie); err == nil {
			u, err := s.sessionUser(ctx, c.Value)
			if err != nil {
				s.log.Warn("session check failed", "error", err)
			}
			if u != nil {
				ctx = WithUser(ctx, u)
			} else {
				http.SetCookie(w, s.cookie(SessionCookie, "", -1))
			}
		}
		if c, err := r.Cookie(setupCookie); err == nil && s.validSetupCookie(c.Value) {
			ctx = context.WithValue(ctx, setupKey, true)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func changesState(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

func (s *Service) sameOrigin(r *http.Request) bool {
	want, err := url.Parse(s.cfg.BaseURL)
	if err != nil {
		return false
	}
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	got, err := url.Parse(src)
	if err != nil || src == "" {
		return false
	}
	return got.Scheme == want.Scheme && got.Host == want.Host
}

func (s *Service) sessionUser(ctx context.Context, tok string) (*User, error) {
	q := New(s.pool)
	sess, err := q.SessionByHash(ctx, hashToken(tok))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.Now()
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) > IdleTimeout {
		return nil, q.DeleteSession(ctx, sess.TokenHash)
	}
	u, err := q.UserByID(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	if u.RolesCheckedAt == nil || now.Sub(*u.RolesCheckedAt) >= RoleRecheck {
		if u, err = s.recheck(ctx, u); err != nil {
			return nil, err
		}
	}
	if u.Status != "active" {
		return nil, q.DeleteUserSessions(ctx, u.ID)
	}
	if now.Sub(sess.LastSeenAt) > time.Minute {
		if err := q.TouchSession(ctx, TouchSessionParams{ID: sess.ID, LastSeenAt: now}); err != nil {
			return nil, err
		}
	}
	return &User{ID: u.ID, Login: u.Login, Name: u.Name, Role: Role(u.Role)}, nil
}

func (s *Service) recheck(ctx context.Context, u UserRow) (UserRow, error) {
	app, err := github.LoadApp(ctx, s.pool, s.secrets)
	if err != nil {
		return u, err
	}
	res, err := s.Resolve(ctx, app, u.Login)
	if err != nil {
		return u, err
	}
	now := s.Now()
	status, role, source := "active", string(res.Role), res.Source
	if !res.Member {
		status, role, source = "removed", u.Role, u.RoleSource
	}
	nu, err := New(s.pool).UpdateUserRole(ctx, UpdateUserRoleParams{ID: u.ID, Role: role, RoleSource: source, Status: status, RolesCheckedAt: &now})
	if err != nil {
		return u, err
	}
	if nu.Role != u.Role || nu.Status != u.Status {
		_ = audit.Write(ctx, s.pool, audit.System, "access.role_change", u.Login, map[string]any{"old": u.Role, "new": nu.Role, "status": nu.Status})
	}
	return nu, nil
}

// SetupSession checks the setup token and returns a cookie that opens the
// setup flow only. It works only while no GitHub App exists (SI-14).
func (s *Service) SetupSession(token string) (*http.Cookie, error) {
	if s.cfg.SetupToken == "" || !hmac.Equal([]byte(token), []byte(s.cfg.SetupToken)) {
		return nil, errors.New("wrong setup token")
	}
	exp := s.Now().Add(2 * time.Hour).Unix()
	v := strconv.FormatInt(exp, 10) + "." + s.sign(exp)
	return s.cookie(setupCookie, v, 2*time.Hour), nil
}

func (s *Service) sign(exp int64) string {
	m := hmac.New(sha256.New, s.cfg.SigningKey)
	_, _ = fmt.Fprintf(m, "setup:%d", exp)
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) validSetupCookie(v string) bool {
	expS, sig, ok := strings.Cut(v, ".")
	exp, err := strconv.ParseInt(expS, 10, 64)
	if !ok || err != nil || s.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.sign(exp)))
}
