// Row-111 live probe: the shipped store code against the production database.
// It uses synthetic keys that no turn uses, and it deletes its own row at the
// end. Read the header of the register row 111 for the claim it measures.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "-watch" {
		watch(os.Args[2])
		return
	}
	dsn := os.Getenv("PROBE_DSN")
	if dsn == "" {
		dsn = os.Getenv("FASTAGENT_STORAGE_DSN")
	}
	if dsn == "" {
		fmt.Println("PROBE_DSN is empty")
		os.Exit(2)
	}
	ctx := context.Background()
	db, err := store.NewDBStore("postgres", dsn)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()

	user, agent := "probe-row111", "probe-row111"
	session := fmt.Sprintf("probe-row111-%d", time.Now().UnixNano())

	fail := func(format string, a ...any) {
		fmt.Printf("FAIL: "+format+"\n", a...)
		_, _ = db.DB().ExecContext(ctx, `DELETE FROM session_turns WHERE user_id = $1`, user)
		os.Exit(1)
	}
	ok := func(format string, a ...any) { fmt.Printf("ok: "+format+"\n", a...) }

	// 1. A fresh acquisition: epoch 1, no stop request.
	epoch, got, err := db.AcquireSessionLease(ctx, user, agent, session, "probe-a/1", 2*time.Second)
	if err != nil || !got || epoch != 1 {
		fail("fresh acquire: epoch=%d ok=%v err=%v", epoch, got, err)
	}
	rec, err := db.GetSessionLease(ctx, user, agent, session)
	if err != nil || rec == nil || rec.CancelRequested != 0 {
		fail("fresh row: rec=%+v err=%v", rec, err)
	}
	ok("fresh acquisition: epoch=%d cancel_requested=%d", rec.Epoch, rec.CancelRequested)

	// 2. A stop request lands on the live possession.
	stamped, err := db.RequestSessionCancel(ctx, user, agent, session)
	if err != nil || !stamped {
		fail("cancel on the live row: ok=%v err=%v", stamped, err)
	}
	ok("a stop request landed on the live possession")

	// 3. The row lapses with no peer: the predicate refuses a late request.
	time.Sleep(2500 * time.Millisecond)
	stale, err := db.RequestSessionCancel(ctx, user, agent, session)
	if err != nil || stale {
		fail("cancel after the lapse: ok=%v err=%v; want false/nil", stale, err)
	}
	if live, err := db.GetSessionLease(ctx, user, agent, session); err != nil || live != nil {
		fail("the row is not filtered as expired: %+v err=%v", live, err)
	}
	ok("the lapsed row is invisible and refuses a late request")

	// 4. THE CLAUSE UNDER TEST: a takeover of the expired row starts at 0.
	epoch2, got, err := db.AcquireSessionLease(ctx, user, agent, session, "probe-b/2", time.Minute)
	if err != nil || !got {
		fail("takeover acquire: epoch=%d ok=%v err=%v", epoch2, got, err)
	}
	rec2, err := db.GetSessionLease(ctx, user, agent, session)
	if err != nil || rec2 == nil {
		fail("takeover row: rec=%+v err=%v", rec2, err)
	}
	if rec2.Epoch != epoch2 || rec2.HolderID != "probe-b/2" {
		fail("takeover row is not the new possession: %+v", rec2)
	}
	if rec2.CancelRequested != 0 {
		fail("the new possession inherited cancel_requested=%d from the lapsed one", rec2.CancelRequested)
	}
	ok("takeover: epoch=%d cancel_requested=%d (the stamp of the lapsed possession did not leak)", rec2.Epoch, rec2.CancelRequested)

	// 5. L5: a stale release cannot free the new possession's row.
	if err := db.ReleaseSessionLease(ctx, user, agent, session, "probe-a/1", epoch); err != nil {
		fail("stale release: %v", err)
	}
	rec3, err := db.GetSessionLease(ctx, user, agent, session)
	if err != nil || rec3 == nil {
		fail("the stale release freed the live row: rec=%+v err=%v", rec3, err)
	}
	ok("the stale release left the live row in place")

	// 6. The guarded release of the current possession removes the row.
	if err := db.ReleaseSessionLease(ctx, user, agent, session, "probe-b/2", epoch2); err != nil {
		fail("current release: %v", err)
	}
	if rec4, err := db.GetSessionLease(ctx, user, agent, session); err != nil || rec4 != nil {
		fail("the current release left the row behind: %+v err=%v", rec4, err)
	}
	ok("the guarded release of the current possession removed the row")

	_, _ = db.DB().ExecContext(ctx, `DELETE FROM session_turns WHERE user_id = $1`, user)
	fmt.Println("ALL-OK")
}

// watch prints the live lease rows every second for the given duration, so a
// turn started through the API can be watched from the outside: when a row
// appears, when its epoch moves, and when it goes away.
func watch(window string) {
	dsn := os.Getenv("PROBE_DSN")
	if dsn == "" {
		dsn = os.Getenv("FASTAGENT_STORAGE_DSN")
	}
	db, err := store.NewDBStore("postgres", dsn)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	d, err := time.ParseDuration(window)
	if err != nil {
		fmt.Println("window:", err)
		os.Exit(2)
	}
	ctx := context.Background()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		rows, err := db.DB().QueryContext(ctx,
			`SELECT agent_id, session_key, holder_id, epoch, expires_at, cancel_requested
			   FROM session_turns ORDER BY acquired_at DESC LIMIT 8`)
		if err != nil {
			fmt.Println("query:", err)
			return
		}
		stamp := time.Now().UTC().Format("15:04:05")
		n := 0
		for rows.Next() {
			var agentID, sessionKey, holder string
			var epoch, cancel int64
			var expires time.Time
			if err := rows.Scan(&agentID, &sessionKey, &holder, &epoch, &expires, &cancel); err != nil {
				fmt.Println("scan:", err)
				break
			}
			n++
			tail := sessionKey
			if len(tail) > 8 {
				tail = "..." + tail[len(tail)-8:]
			}
			fmt.Printf("%s row %d agent=%s session=%s holder=%s epoch=%d ttl=%ds cancel=%d\n",
				stamp, n, agentID, tail, holder, epoch, int(time.Until(expires).Seconds()), cancel)
		}
		rows.Close()
		if n == 0 {
			fmt.Printf("%s no live lease row\n", stamp)
		}
		time.Sleep(time.Second)
	}
}
