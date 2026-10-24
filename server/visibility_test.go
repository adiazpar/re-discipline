package server

import (
	"context"
	"errors"
	"testing"

	kb "github.com/adiazpar/re-discipline/retrieval/community"
	"github.com/adiazpar/re-discipline/server/database"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCommunityVisibility(t *testing.T) {
	s, h := fixture(t)
	owner, member, outsider := id(), id(), id()
	doom := create(t, h, owner, "doom-visibility", "private", "trusted")
	godzilla := create(t, h, owner, "godzilla-visibility", "private", "trusted")
	if doom.Visibility != "private" || godzilla.Visibility != "private" {
		t.Fatal("creation without visibility must default to private")
	}
	for _, visibility := range []string{"public", "unlisted"} {
		draft := doom
		draft.Slug, draft.Visibility = "invalid-creation", visibility
		op(t, h, owner, "community.create", "", draft, 400)
	}
	inv := unpack[map[string]any](t, op(t, h, owner, "invite.create", doom.ID, map[string]any{"role": "maintainer"}, 200))
	op(t, h, member, "invite.redeem", "", map[string]any{"token": inv["token"]}, 200)
	checkList := func(user string, want ...string) {
		t.Helper()
		got := unpack[[]kb.Library](t, op(t, h, user, "community.list", "", map[string]bool{"discover": true}, 200))
		if len(got) != len(want) {
			t.Fatalf("discovery returned %d communities, want %d: %+v", len(got), len(want), got)
		}
		for _, l := range got {
			found := false
			for _, expected := range want {
				found = found || l.ID == expected
			}
			if !found {
				t.Fatalf("discovery leaked community %s", l.ID)
			}
		}
	}
	checkList("")
	checkList(outsider)
	checkList(member, doom.ID)
	for _, address := range []string{godzilla.ID, godzilla.Slug} {
		op(t, h, member, "community.get", address, nil, 404)
	}
	update := func(l kb.Library, visibility string) map[string]any {
		return map[string]any{"name": l.Name, "visibility": visibility, "policy": l.Policy, "expected_version": l.PolicyVersion}
	}
	op(t, h, owner, "community.update", doom.ID, update(doom, "unlisted"), 400)
	op(t, h, member, "community.update", doom.ID, update(doom, "public"), 403)

	// Database permissions must protect visibility without blocking the sequence
	// updates maintainers need when reviewing findings.
	ctx := context.Background()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE re_community_app"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('app.user_id',$1,true)", member); err != nil {
		t.Fatal(err)
	}
	if _, err = nextSequence(ctx, tx, doom.ID); err != nil {
		t.Fatalf("maintainer sequence update: %v", err)
	}
	_, err = tx.Exec(ctx, "UPDATE community.libraries SET visibility='public' WHERE id=$1", doom.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("maintainer visibility write: %v", err)
	}
	tx.Rollback(ctx)

	public := unpack[kb.Library](t, op(t, h, owner, "community.update", doom.ID, update(doom, "public"), 200))
	checkList("", doom.ID)
	checkList(outsider, doom.ID)
	for _, address := range []string{doom.ID, doom.Slug} {
		op(t, h, "", "community.get", address, nil, 200)
	}
	op(t, h, outsider, "community.update", doom.ID, update(public, "private"), 403)
	op(t, h, owner, "community.update", doom.ID, update(public, "private"), 200)
	// A stale settings page cannot accidentally make a newly private corpus public.
	op(t, h, owner, "community.update", doom.ID, update(public, "public"), 409)
	checkList("")
	checkList(outsider)
	checkList(member, doom.ID)
	for _, address := range []string{doom.ID, doom.Slug} {
		op(t, h, "", "community.get", address, nil, 404)
		op(t, h, outsider, "community.get", address, nil, 404)
		op(t, h, member, "community.get", address, nil, 200)
	}
	op(t, h, owner, "member.remove", doom.ID, map[string]string{"user_id": member}, 200)
	checkList(member)
	op(t, h, member, "community.get", doom.ID, nil, 404)
}

func TestVisibilityMigration(t *testing.T) {
	s, h := fixture(t)
	ctx, owner := context.Background(), id()
	legacy := create(t, h, owner, "legacy-visibility", "private", "trusted")
	public := create(t, h, owner, "public-visibility", "public", "trusted")
	// Recreate the old visibility state in this disposable fixture database.
	_, err := s.DB.Exec(ctx, `
DROP TRIGGER guard_library_visibility ON community.libraries;
DROP FUNCTION community.guard_library_visibility();
ALTER TABLE community.libraries DROP CONSTRAINT libraries_visibility_check;
ALTER TABLE community.libraries ADD CONSTRAINT libraries_visibility_check CHECK (visibility IN ('private','public','unlisted'));
ALTER TABLE community.libraries ALTER COLUMN visibility DROP DEFAULT;
CREATE OR REPLACE FUNCTION community.can_read(c uuid) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,community AS $$
 SELECT EXISTS(SELECT 1 FROM community.libraries WHERE id=c AND (owner_id=community.actor() OR visibility IN ('public','unlisted') OR community.member_role(c) IS NOT NULL))
$$;
DELETE FROM community.schema_migrations WHERE name='017_private_public_visibility.sql';`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE community.libraries SET visibility='unlisted' WHERE id=$1", legacy.ID); err != nil {
		t.Fatal(err)
	}
	op(t, h, "", "community.get", legacy.ID, nil, 200)
	for i := 0; i < 2; i++ {
		if err = database.Migrate(ctx, s.DB); err != nil {
			t.Fatal(err)
		}
		got := unpack[kb.Library](t, op(t, h, owner, "community.get", legacy.ID, nil, 200))
		if got.Visibility != "private" || got.PolicyVersion != legacy.PolicyVersion+1 {
			t.Fatalf("legacy visibility migration: %+v", got)
		}
		op(t, h, "", "community.get", legacy.ID, nil, 404)
		got = unpack[kb.Library](t, op(t, h, "", "community.get", public.ID, nil, 200))
		if got.Visibility != "public" || got.PolicyVersion != public.PolicyVersion {
			t.Fatal("migration changed an existing public community")
		}
	}
	var visibility string
	err = s.DB.QueryRow(ctx, `INSERT INTO community.libraries(id,owner_id,name,slug,policy) VALUES($1,$2,'Default visibility','default-visibility',$3) RETURNING visibility`, id(), owner, raw(legacy.Policy)).Scan(&visibility)
	if err != nil || visibility != "private" {
		t.Fatalf("database default: visibility=%q error=%v", visibility, err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE community.libraries SET visibility='unlisted' WHERE id=$1", legacy.ID); err == nil {
		t.Fatal("database accepted retired visibility")
	}
}
