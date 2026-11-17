package services

import (
	"path/filepath"
	"sync"
	"testing"

	"dbviewer/internal/model"
)

// Parallel first calls on a lazily connected connection share one pool.
func TestLazyConnectConcurrent(t *testing.T) {
	f := newFixture(t)
	c := f.save(model.ConnectionConfig{Name: "sq", Driver: model.DriverSQLite, FilePath: filepath.Join(repoRoot(), "testdata", "dummy.sqlite")})
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: c.ID, Table: "sales_order", Limit: 50}); err != nil {
				errs <- err
			}
			if _, err := f.ss.SearchSchema(f.ctx, c.ID, "", false); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
