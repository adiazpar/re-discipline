package server

import "context"

// addMemberLogins enriches an already authorized member.list result after its
// transaction releases the connection. Account emails never leave this lookup.
func (s *Server) addMemberLogins(ctx context.Context, members []map[string]string) error {
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member["user_id"])
	}
	rows, err := s.DB.Query(ctx, `SELECT id::text,login FROM community.accounts WHERE id=ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	logins := map[string]string{}
	for rows.Next() {
		var uid, login string
		if err = rows.Scan(&uid, &login); err != nil {
			return err
		}
		logins[uid] = login
	}
	for _, member := range members {
		member["login"] = logins[member["user_id"]]
	}
	return rows.Err()
}
