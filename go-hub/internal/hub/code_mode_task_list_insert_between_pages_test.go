package hub

import (
    "testing"
    "time"
)

// A new task arriving between page reads must not move the keyset cursor.
// The old offset cursor duplicates the first result here.
func TestTaskListInsertBetweenPagesDoesNotDuplicateOrSkip(t *testing.T) {
    s, r := taskBridgeFixture(t)
    original := map[string]bool{}
    for i := 0; i < 3; i++ {
        original[startBridgeTask(t, s, r)] = true
        time.Sleep(15 * time.Millisecond)
    }

    first, err := s.listOwnedTasksForRequest(r, 1, "")
    if err != nil {
        t.Fatal(err)
    }
    firstRows := outputRows(first["tasks"])
    if len(firstRows) != 1 {
        t.Fatalf("expected one first-page task: %#v", first)
    }
    seen := map[string]bool{}
    firstID := firstString(mapValue(firstRows[0]), "task_id")
    seen[firstID] = true
    cursor := firstString(first, "next_cursor")
    if cursor == "" {
        t.Fatalf("missing continuation cursor: %#v", first)
    }

    // This task is newer than the page snapshot and must not cause a
    // duplicate or shift any of the original unreturned entries.
    time.Sleep(20 * time.Millisecond)
    newer := startBridgeTask(t, s, r)
    if original[newer] {
        t.Fatal("fixture created a duplicate task identifier")
    }

    for page := 0; page < 5 && cursor != ""; page++ {
        next, err := s.listOwnedTasksForRequest(r, 1, cursor)
        if err != nil {
            t.Fatal(err)
        }
        for _, item := range outputRows(next["tasks"]) {
            id := firstString(mapValue(item), "task_id")
            if seen[id] {
                t.Fatalf("task %s repeated after insertion: %#v", id, next)
            }
            if id == newer {
                t.Fatalf("newer task entered an already-started scan: %#v", next)
            }
            if !original[id] {
                t.Fatalf("unknown task entered scan: %s", id)
            }
            seen[id] = true
        }
        cursor = firstString(next, "next_cursor")
    }
    if len(seen) != len(original) {
        t.Fatalf("keyset scan lost tasks: saw %d/%d: %#v", len(seen), len(original), seen)
    }
}
