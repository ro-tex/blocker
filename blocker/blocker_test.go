package blocker

import (
	"context"
	"fmt"
	"io/ioutil"
	"testing"
	"time"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// mockSkyd is a helper struct that implements the skyd API, all methods are
// essentially a no-op except for 'BlockSkylinks' which keeps track of the
// arguments with which it is called
type mockSkyd struct {
	BlockSkylinksReqs [][]string
}

// BlockSkylinks adds the given skylinks to the block list.
func (api *mockSkyd) BlockSkylinks(skylinks []string) error {
	api.BlockSkylinksReqs = append(api.BlockSkylinksReqs, skylinks)

	// check whether the caller expects an error to be thrown
	for _, sl := range skylinks {
		if sl == "throwerror" {
			return errors.New(unableToUpdateBlocklistErrStr)
		}
	}
	return nil
}

// IsSkydUp returns true if the skyd API instance is up.
func (api *mockSkyd) IsSkydUp() bool {
	return true
}

// ResolveSkylink tries to resolve the given skylink to a V1 skylink.
func (api *mockSkyd) ResolveSkylink(skylink string) (string, error) {
	return skylink, nil
}

// TestBlocker runs the blocker unit tests
func TestBlocker(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "BlockSkylinks",
			test: testBlockSkylinks,
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.test)
	}
}

// testBlockSkylinks is a unit test that covers the 'blockSkylinks' method.
func testBlockSkylinks(t *testing.T) {
	// create a mock skyd api
	api := &mockSkyd{}

	// create the blocker
	blocker, err := newTestBlocker("BlockSkylinks", api)
	if err != nil {
		panic(err)
	}

	// defer a db close
	defer func() {
		if err := blocker.staticDB.Close(); err != nil {
			t.Error(err)
		}
	}()

	ts := time.Now().UTC()
	ts = ts.Truncate(time.Second)

	// create a list of 16 skylinks, where the 10th skylink is one that triggers
	// an error to be thrown in skyd, this will ensure the blocker tries:
	// - all skylinks in 1 batch
	// - a batch size of 10, which still fails
	// - all skylinks in a batch size of 1, which returns the failing skylink
	var skylinks []database.BlockedSkylink
	var i int
	for ; i < 9; i++ {
		ts = ts.Add(time.Minute)
		skylinks = append(skylinks, database.BlockedSkylink{Skylink: fmt.Sprintf("skylink_%d", i), TimestampAdded: ts})
	}

	// the last skylink before the failure should be the latest timestamp set,
	// so save this timestamp as an expected value for later
	expectedLatest := ts
	skylinks = append(skylinks, database.BlockedSkylink{Skylink: "throwerror"})
	for ; i < 15; i++ {
		ts = ts.Add(time.Minute)
		skylinks = append(skylinks, database.BlockedSkylink{Skylink: fmt.Sprintf("skylink_%d", i), TimestampAdded: ts})
	}

	blocked, failed, err := blocker.blockSkylinks(skylinks)
	if err != nil {
		t.Fatal("unexpected error thrown", err)
	}
	// assert blocked and failed are returned correctly
	if blocked != 15 {
		t.Fatalf("unexpected return values for blocked, %v != 15", blocked)
	}
	if failed != 1 {
		t.Fatalf("unexpected return values for failed, %v != 1", failed)
	}

	// assert 18 requests in total happen to skyd, batch size 100, 10 and 1
	if len(api.BlockSkylinksReqs) != 18 {
		t.Fatalf("unexpected amount of calls to Skyd block endpoint, %v != 18", len(api.BlockSkylinksReqs))
	}
	if len(api.BlockSkylinksReqs[0]) != 16 {
		t.Fatalf("unexpected first batch size, %v != 16", len(api.BlockSkylinksReqs[0]))
	}
	if len(api.BlockSkylinksReqs[1]) != 10 {
		t.Fatalf("unexpected second batch size, %v != 10", len(api.BlockSkylinksReqs[1]))
	}
	for r := 2; r < 18; r++ {
		if len(api.BlockSkylinksReqs[r]) != 1 {
			t.Fatalf("unexpected batch size for req %d, %v != 1", r, len(api.BlockSkylinksReqs[r]))
		}
	}

	// assert the latest block timestamp has been set to the timestamp of the
	// last succeeding skylink before the failure
	latest, err := blocker.staticDB.LatestBlockTimestamp()
	if err != nil {
		t.Fatal("failed to fetch latest block timestamp", err)
	}
	if latest != expectedLatest {
		t.Fatalf("latest block timestamp not updated to last succeeding skylink timestamp added, %v != %v", latest, expectedLatest)
	}
}

// newTestBlocker returns a new blocker instance
func newTestBlocker(dbName string, api skyd.API) (*Blocker, error) {
	// create a nil logger
	logger := logrus.New()
	logger.Out = ioutil.Discard

	// create database
	db, err := database.NewCustomDB(context.Background(), "mongodb://localhost:37017", dbName, options.Credential{
		Username: "admin",
		Password: "aO4tV5tC1oU3oQ7u",
	}, logger)
	if err != nil {
		return nil, err
	}

	// create the blocker
	blocker, err := New(context.Background(), api, db, logger)
	if err != nil {
		return nil, err
	}
	return blocker, nil
}
