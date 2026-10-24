package server

import (
	"context"
	"strings"
	"testing"
)

func TestMemberUsernamesRemainScopedAndEmailsPrivate(t *testing.T) {
	s, h := fixture(t)
	owner, outsider := id(), id()
	for i, user := range []string{owner, outsider} {
		if _, err := s.DB.Exec(context.Background(), `INSERT INTO community.accounts(id,github_id,login,email) VALUES($1,$2,$3,$4)`, user, i+100, []string{"library-owner", "outside-user"}[i], "private@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	l := create(t, h, owner, "member-names", "private", "trusted")
	body := op(t, h, owner, "member.list", l.ID, map[string]any{}, 200)
	if !strings.Contains(string(body), "library-owner") || strings.Contains(string(body), "outside-user") || strings.Contains(string(body), "private@example.test") {
		t.Fatal("member identity scope or privacy regression", string(body))
	}
	op(t, h, outsider, "member.list", l.ID, map[string]any{}, 404)
	op(t, h, "", "member.list", l.ID, map[string]any{}, 404)
}
