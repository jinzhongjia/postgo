package storage

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// TestHistoryQuerySpeed checks DESIGN's budget for C5: queries of a history
// of 100,000 entries take less than 50 ms.
func TestHistoryQuerySpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	d, _ := open(t)
	err := d.tx(func(tx *sql.Tx) error {
		st, err := tx.Prepare(`INSERT INTO history (time, collection_id, request_id, source, client, method, url, status, duration, size, err)
			VALUES (?, ?, ?, 1, '', 'GET', ?, 200, 0, 0, '')`)
		if err != nil {
			return err
		}
		for i := range 100_000 {
			if _, err := st.Exec(int64(i), fmt.Sprint("c", i%5), fmt.Sprintf("r%d.postgo", i%500), fmt.Sprintf("https://api.example.com/items/%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []HistoryQuery{
		{},
		{CollectionID: "c3"},
		{CollectionID: "c3", RequestID: "r3.postgo"},
		{Text: "items/9999"},
		{CollectionID: "c1", Source: SourceUser, Before: 50_000},
	} {
		start := time.Now()
		if _, err := d.History(q); err != nil {
			t.Fatal(err)
		}
		if el := time.Since(start); el > 50*time.Millisecond {
			t.Errorf("%+v took %v", q, el)
		} else {
			t.Logf("%+v: %v", q, el)
		}
	}
}
