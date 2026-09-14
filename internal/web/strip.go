package web

import (
	"context"
	"errors"
	"sort"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// downSince returns the start of the open incident of a monitor in unix
// seconds, or 0 when it has none.
func (s *Server) downSince(ctx context.Context, id int64) (int64, error) {
	inc, err := s.store.CurrentIncident(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return inc.StartedAt.Unix(), nil
}

// sortStrip orders the rows of the DOWN strip by outage start, newest first.
// Equal starts go in monitor id order. live.js places a row that goes down
// by the same rule, so a reload shows the live order.
func sortStrip(rows []monitorRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].DownSince != rows[j].DownSince {
			return rows[i].DownSince > rows[j].DownSince
		}
		return rows[i].ID < rows[j].ID
	})
}
