//go:build integration

package database_test

import (
	"path/filepath"
	"testing"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/telemetry/telemetrytest"
)

func TestPoolSize(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)

	url := databasetest.NewDB(t).Pool().Config().ConnString()

	tests := []struct {
		name string
		url  string
		opts database.Options
		want int32
	}{
		{
			name: "from the options",
			url:  url,
			opts: database.Options{
				MaxConnections: 7,
			},
			want: 7,
		},
		{
			name: "the URL's pool_max_conns wins",
			url:  url + "&pool_max_conns=3",
			opts: database.Options{
				MaxConnections: 7,
			},
			want: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, err := database.OpenWith(t.Context(), tt.url, tt.opts)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(db.Close)

			if got := db.Pool().Config().MaxConns; got != tt.want {
				t.Errorf("MaxConns = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSQLitePoolSeriesCountOnlyTheAppPool(t *testing.T) {
	before := telemetrytest.Value(t, "relic_db_pool_max", nil)

	db, err := database.Open(t.Context(), "sqlite://"+filepath.Join(t.TempDir(), "relic.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	if added := telemetrytest.Value(t, "relic_db_pool_max", nil) - before; added != 8 {
		t.Errorf("opening a SQLite database added %v to relic_db_pool_max, want its 8 app connections", added)
	}
}
