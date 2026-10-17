//go:build unix

package localdisk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockTakesTheFileAtThePathAfterItIsRemoved(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "run.json.lock")

	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan error, 1)

	go func() {
		unlockSecond, err := Lock(path)
		if err == nil {
			_, err = os.Stat(path)

			unlockSecond()
		}

		acquired <- err
	}()

	time.Sleep(200 * time.Millisecond)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	unlock()

	if err := <-acquired; err != nil {
		t.Errorf("the waiting Lock holds a file that is no longer at the path: %v", err)
	}
}
