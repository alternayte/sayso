package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/github"
)

// Role is a user role (§17).
type Role string

const (
	Viewer     Role = "viewer"
	Maintainer Role = "maintainer"
	Admin      Role = "admin"
)

func (r Role) rank() int {
	switch r {
	case Admin:
		return 3
	case Maintainer:
		return 2
	case Viewer:
		return 1
	}
	return 0
}

// AtLeast reports whether r has the rights of min.
func (r Role) AtLeast(min Role) bool { return r.rank() >= min.rank() }

// ParseRole accepts the three role names.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if r.rank() == 0 {
		return "", fmt.Errorf("unknown role %q: use admin, maintainer or viewer", s)
	}
	return r, nil
}

// Resolution is the outcome of a role check against GitHub.
type Resolution struct {
	Member bool
	Role   Role
	Source string // grant, team or default
}

// Resolve checks organization membership and team mappings with an
// installation token, and combines them with grants. The highest role wins.
// A member with no grant and no mapped team is a viewer.
func (s *Service) Resolve(ctx context.Context, app *github.App, login string) (Resolution, error) {
	gh, err := s.GitHub(ctx)
	if err != nil {
		return Resolution{}, err
	}
	tok, err := gh.OrgInstallationToken(ctx, app)
	if err != nil {
		return Resolution{}, err
	}
	member, err := gh.IsOrgMember(ctx, tok, app.Org, login)
	if err != nil || !member {
		return Resolution{}, err
	}
	res := Resolution{Member: true, Role: Viewer, Source: "default"}
	q := New(s.pool)
	g, err := q.GrantByLogin(ctx, login)
	switch {
	case err == nil:
		res.Role, res.Source = Role(g.Role), "grant"
	case !errors.Is(err, pgx.ErrNoRows):
		return Resolution{}, err
	}
	teams, err := q.TeamRoles(ctx)
	if err != nil {
		return Resolution{}, err
	}
	for _, t := range teams {
		if !Role(t.Role).AtLeast(res.Role) || (Role(t.Role) == res.Role && res.Source != "default") {
			continue
		}
		in, err := gh.IsTeamMember(ctx, tok, app.Org, t.TeamSlug, login)
		if err != nil {
			return Resolution{}, err
		}
		if in {
			res.Role, res.Source = Role(t.Role), "team"
		}
	}
	return res, nil
}

func (s *Service) isBootstrapAdmin(login string) bool {
	for _, b := range s.cfg.BootstrapAdmins {
		if strings.EqualFold(b, login) {
			return true
		}
	}
	return false
}
