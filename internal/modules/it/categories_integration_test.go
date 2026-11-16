package it

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/database"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestCategoryKey(t *testing.T) {
	none := func(string) bool { return false }
	cases := []struct {
		name  string
		taken func(string) bool
		want  string
	}{
		{"Server", none, "server"},
		{"Überwachungs-Kamera", none, "ueberwachungs-kamera"},
		{"Größe ÄÖÜ ß", none, "groesse-aeoeue-ss"},
		{"  Mehrere   Leerzeichen  ", none, "mehrere-leerzeichen"},
		{"A/B & C (neu)!", none, "a-b-c-neu"},
		{"---", none, "kategorie"},
		{"", none, "kategorie"},
		{"Kamera", func(k string) bool { return k == "kamera" }, "kamera-2"},
		{"Kamera", func(k string) bool { return k == "kamera" || k == "kamera-2" }, "kamera-3"},
		{strings.Repeat("a", 60), none, strings.Repeat("a", 40)},
	}
	for _, c := range cases {
		if got := CategoryKey(c.name, c.taken); got != c.want {
			t.Errorf("CategoryKey(%q) = %q, erwartet %q", c.name, got, c.want)
		}
	}
	// Länge 40 gilt einschließlich Suffix, kein „-“ vor dem Suffix.
	long := strings.Repeat("a", 60)
	base := strings.Repeat("a", 40)
	got := CategoryKey(long, func(k string) bool { return k == base })
	if len(got) > 40 || !strings.HasSuffix(got, "-2") {
		t.Errorf("lang mit Kollision: %q", got)
	}
	got = CategoryKey(strings.Repeat("a", 38)+"-b", func(k string) bool { return k == strings.Repeat("a", 38)+"-b" })
	if len(got) > 40 || strings.Contains(got, "--") || !strings.HasSuffix(got, "-2") {
		t.Errorf("Suffix: %q", got)
	}
	got = CategoryKey(strings.Repeat("a", 37)+"-bb", func(k string) bool { return k == strings.Repeat("a", 37)+"-bb" })
	if len(got) > 40 || strings.Contains(got, "--") {
		t.Errorf("Bindestrich vor Suffix: %q", got)
	}
}

func uniq() string { return fmt.Sprintf("t%d", time.Now().UnixNano()) }

// newCat legt eine eigene Kategorie an und räumt sie (nach den Assets) wieder auf.
func newCat(t *testing.T, repo *Repository, pool *pgxpool.Pool, name string) *Category {
	t.Helper()
	c := &Category{Name: name, Icon: "ti-camera", SortOrder: 900, MonitorDefault: true}
	if err := repo.CreateCategory(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM it_asset_categories WHERE key=$1 AND NOT builtin`, c.Key)
	})
	return c
}

func newAsset(t *testing.T, repo *Repository, pool *pgxpool.Pool, typ AssetType) *Asset {
	t.Helper()
	ctx := context.Background()
	var uid string
	if err := pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&uid); err != nil {
		t.Skip("kein Benutzer in der Test-DB")
	}
	a := &Asset{Name: "kat-test-" + uniq(), Type: typ, CreatedBy: uid}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM it_assets WHERE id=$1`, a.ID) })
	return a
}

func TestCategories(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	name := "Überwachungs-Kamera " + uniq()

	c := newCat(t, repo, pool, name)
	if !strings.HasPrefix(c.Key, "ueberwachungs-kamera-t") || c.Builtin || !c.Active {
		t.Fatalf("Anlage: %+v", c)
	}
	// Eingaben builtin/active werden ignoriert.
	x := &Category{Name: "X " + uniq(), Builtin: true, Active: false}
	if err := repo.CreateCategory(ctx, x); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM it_asset_categories WHERE key=$1`, x.Key) })
	if got, _ := repo.Category(ctx, x.Key); got.Builtin || !got.Active {
		t.Errorf("builtin/active nicht erzwungen: %+v", got)
	}

	t.Run("Umbenennen behält Key", func(t *testing.T) {
		five := 5
		c.Name = "Neu " + uniq()
		c.GraceMinutes = &five
		c.MonitorDefault = false
		c.Active = false // darf ignoriert werden
		c.Builtin = true // darf ignoriert werden
		if err := repo.UpdateCategory(ctx, c); err != nil {
			t.Fatal(err)
		}
		got, err := repo.Category(ctx, c.Key)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != c.Name || got.GraceMinutes == nil || *got.GraceMinutes != 5 || got.MonitorDefault || !got.Active || got.Builtin {
			t.Errorf("nach Update: %+v", got)
		}
		c.Active, c.Builtin = true, false
	})

	t.Run("gleicher Name ergibt neuen Key", func(t *testing.T) {
		a := newCat(t, repo, pool, "Doppel")
		b := newCat(t, repo, pool, "Doppel")
		if a.Key == b.Key {
			t.Fatalf("gleicher Key %q", a.Key)
		}
	})

	t.Run("gleichzeitig gleicher Name", func(t *testing.T) {
		nm := "Parallel " + uniq()
		var wg sync.WaitGroup
		keys := make([]string, 6)
		errs := make([]error, 6)
		for i := range keys {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cc := &Category{Name: nm}
				errs[i] = repo.CreateCategory(ctx, cc)
				keys[i] = cc.Key
			}()
		}
		wg.Wait()
		seen := map[string]bool{}
		for i := range keys {
			if errs[i] != nil {
				t.Errorf("Lauf %d: %v", i, errs[i])
				continue
			}
			if seen[keys[i]] {
				t.Errorf("doppelter Key %q", keys[i])
			}
			seen[keys[i]] = true
			k := keys[i]
			t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM it_asset_categories WHERE key=$1`, k) })
		}
	})

	t.Run("Liste und Assets-Zähler", func(t *testing.T) {
		cc := newCat(t, repo, pool, "Zähler")
		newAsset(t, repo, pool, AssetType(cc.Key))
		list, err := repo.Categories(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for i, e := range list {
			if e.Key == cc.Key {
				found = true
				if e.Assets != 1 {
					t.Errorf("Assets = %d", e.Assets)
				}
			}
			if i > 0 && list[i-1].SortOrder > e.SortOrder {
				t.Errorf("nicht sortiert")
			}
		}
		if !found {
			t.Error("Kategorie fehlt in Liste")
		}
		if err := repo.SetCategoryActive(ctx, cc.Key, false); err != nil {
			t.Fatal(err)
		}
		list, _ = repo.Categories(ctx, false)
		for _, e := range list {
			if e.Key == cc.Key {
				t.Error("ausgeblendete Kategorie in aktiver Liste")
			}
		}
		list, _ = repo.Categories(ctx, true)
		ok := false
		for _, e := range list {
			ok = ok || e.Key == cc.Key
		}
		if !ok {
			t.Error("ausgeblendete fehlt in Gesamtliste")
		}
	})

	t.Run("builtin abgewiesen", func(t *testing.T) {
		if err := repo.SetCategoryActive(ctx, "network", false); !errors.Is(err, ErrCategoryBuiltin) {
			t.Errorf("Ausblenden: %v", err)
		}
		if err := repo.DeleteCategory(ctx, "other"); !errors.Is(err, ErrCategoryBuiltin) {
			t.Errorf("Löschen: %v", err)
		}
	})

	t.Run("Löschen benutzt und unbenutzt", func(t *testing.T) {
		cc := newCat(t, repo, pool, "Löschtest")
		newAsset(t, repo, pool, AssetType(cc.Key))
		if err := repo.DeleteCategory(ctx, cc.Key); !errors.Is(err, ErrCategoryInUse) {
			t.Errorf("benutzt: %v", err)
		}
		un := newCat(t, repo, pool, "Unbenutzt")
		if err := repo.DeleteCategory(ctx, un.Key); err != nil {
			t.Errorf("unbenutzt: %v", err)
		}
		if _, err := repo.Category(ctx, un.Key); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("nach Löschen: %v", err)
		}
	})

	t.Run("unbekannt ist nicht gefunden", func(t *testing.T) {
		if _, err := repo.Category(ctx, "gibt-es-nicht"); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("Category: %v", err)
		}
		if err := repo.UpdateCategory(ctx, &Category{Key: "gibt-es-nicht", Name: "x"}); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("Update: %v", err)
		}
		if err := repo.SetCategoryActive(ctx, "gibt-es-nicht", true); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("Active: %v", err)
		}
		if err := repo.DeleteCategory(ctx, "gibt-es-nicht"); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("Delete: %v", err)
		}
		if err := repo.SetAssetGrace(ctx, "00000000-0000-0000-0000-000000000000", nil); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("Grace: %v", err)
		}
	})

	t.Run("ungültige Werte", func(t *testing.T) {
		zero, big := 0, 1441
		bad := []*Category{
			{Name: "   "},
			{Name: strings.Repeat("ä", 61)},
			{Name: "ok", Icon: "camera"},
			{Name: "ok", Icon: "ti-Camera"},
			{Name: "ok", GraceMinutes: &zero},
			{Name: "ok", GraceMinutes: &big},
		}
		for i, b := range bad {
			if err := repo.CreateCategory(ctx, b); !errors.Is(err, ErrCategoryInvalid) {
				t.Errorf("Create %d: %v", i, err)
			}
			b.Key = c.Key
			if err := repo.UpdateCategory(ctx, b); !errors.Is(err, ErrCategoryInvalid) {
				t.Errorf("Update %d: %v", i, err)
			}
		}
		// 60 Zeichen mit Umlauten sind erlaubt (Zeichen, nicht Bytes).
		ok := &Category{Name: strings.Repeat("ä", 60)}
		if err := repo.CreateCategory(ctx, ok); err != nil {
			t.Errorf("60 Zeichen: %v", err)
		} else {
			t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM it_asset_categories WHERE key=$1`, ok.Key) })
		}
	})

	t.Run("Asset mit unbekanntem Typ", func(t *testing.T) {
		var uid string
		if err := pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&uid); err != nil {
			t.Skip("kein Benutzer")
		}
		a := &Asset{Name: "kat-test-" + uniq(), Type: "gibt-es-nicht", CreatedBy: uid}
		if err := repo.Create(ctx, a); !errors.Is(err, ErrCategoryInvalid) {
			t.Errorf("Create: %v", err)
		}
		good := newAsset(t, repo, pool, "other")
		err := repo.UpdateDetails(ctx, good.ID, &UpdateDetailsInput{Name: good.Name, Type: "gibt-es-nicht"})
		if !errors.Is(err, ErrCategoryInvalid) {
			t.Errorf("UpdateDetails: %v", err)
		}
		// Anderer Fremdschlüssel bleibt, was er ist.
		bogus := "00000000-0000-0000-0000-000000000000"
		err = repo.UpdateDetails(ctx, good.ID, &UpdateDetailsInput{Name: good.Name, Type: "other", AssignedTo: &bogus})
		if err == nil || errors.Is(err, ErrCategoryInvalid) {
			t.Errorf("anderer FK: %v", err)
		}
	})
}

func TestAssetGrace(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	a := newAsset(t, repo, pool, "other")

	read := func() *int {
		var m *int
		if err := pool.QueryRow(ctx, `SELECT net_grace_minutes FROM it_assets WHERE id=$1`, a.ID).Scan(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	ten := 10
	if err := repo.SetAssetGrace(ctx, a.ID, &ten); err != nil {
		t.Fatal(err)
	}
	if m := read(); m == nil || *m != 10 {
		t.Errorf("gesetzt: %v", m)
	}
	if err := repo.SetAssetGrace(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if m := read(); m != nil {
		t.Errorf("geleert: %v", *m)
	}
	zero, big := 0, 1441
	for _, v := range []*int{&zero, &big} {
		if err := repo.SetAssetGrace(ctx, a.ID, v); !errors.Is(err, ErrCategoryInvalid) {
			t.Errorf("ungültig %d: %v", *v, err)
		}
	}
}
