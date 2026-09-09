package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateConcurrentPublicationNeverOverwrites(t *testing.T) {
	dir, opts := generateFixture(t)
	const writers = 12
	start := make(chan struct{})
	results := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			<-start
			results <- Generate(context.Background(), []string{"*.feature"}, opts)
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < writers; i++ {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("want exactly one successful publisher, got %d", successes)
	}
	if _, err := Compile(opts.Output); err != nil {
		t.Fatalf("published file must be complete: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if matched, _ := filepath.Match(".godog-workflow-*", entry.Name()); matched {
			t.Errorf("candidate was not removed: %s", entry.Name())
		}
	}
}
