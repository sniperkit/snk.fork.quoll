/*
Sniperkit-Bot
- Status: analyzed
*/

package evtstore

import (
	"testing"
	"time"

	"github.com/blang/vfs/memfs"
	"github.com/stretchr/testify/require"

	"github.com/sniperkit/snk.fork.quoll/discr"
	"github.com/sniperkit/snk.fork.quoll/timeutil"
)

func init() {
	epoch := time.Unix(1483228900, 0)
	timeutil.MockNow(epoch)
	discr.NewDiscrminator = func() discr.Discrminator {
		return &mockDiscr{}
	}
}

type mockDiscr struct {
}

func (md *mockDiscr) SceneOf(eventBody discr.EventBody) discr.Scene {
	return discr.Scene{}
}

func reset() {
	fs = memfs.Create()
	fs.Mkdir("/tmp", 0666)
}

func Test_add_one(t *testing.T) {
	reset()
	should := require.New(t)
	var testStore = NewStore("/tmp")
	err := testStore.Add([]byte(`{"url":"/hello"}`))
	should.Nil(err)
	testStore.flushInputQueue()
	dir, _ := fs.ReadDir("/tmp")
	should.Len(dir, 1)
	should.Equal("201701010800", dir[0].Name())
}

func Test_add_multiple(t *testing.T) {
	reset()
	should := require.New(t)
	var testStore = NewStore("/tmp")
	err := testStore.Add([]byte(`{"url":"/hello"}`))
	should.Nil(err)
	err = testStore.Add([]byte(`{"url":"/hello"}`))
	should.Nil(err)
	testStore.flushInputQueue()
	dir, _ := fs.ReadDir("/tmp")
	should.Len(dir, 1)
	should.Equal("201701010800", dir[0].Name())
}

func Test_rotation_happen_between_flush(t *testing.T) {
	reset()
	should := require.New(t)
	var testStore = NewStore("/tmp")
	should.Nil(testStore.Add([]byte(`{"url":"/hello"}`)))
	testStore.flushInputQueue()
	timeutil.MockNow(timeutil.Now().Add(time.Hour))
	should.Nil(testStore.Add([]byte(`{"url":"/hello"}`)))
	testStore.flushInputQueue()
	dir, _ := fs.ReadDir("/tmp")
	should.Len(dir, 2)
	should.Equal("201701010800", dir[0].Name())
	should.Equal("201701010900", dir[1].Name())
}

func Test_rotation_happen_within_flush(t *testing.T) {
	reset()
	should := require.New(t)
	var testStore = NewStore("/tmp")
	should.Nil(testStore.Add([]byte(`{"url":"/hello"}`)))
	timeutil.MockNow(timeutil.Now().Add(time.Hour))
	should.Nil(testStore.Add([]byte(`{"url":"/hello"}`)))
	testStore.flushInputQueue()
	dir, _ := fs.ReadDir("/tmp")
	should.Len(dir, 2)
	should.Equal("201701010800", dir[0].Name())
	should.True(dir[0].Size() > 0)
	should.Equal("201701010900", dir[1].Name())
	should.True(dir[1].Size() > 0)
}

func Test_clean(t *testing.T) {
	reset()
	should := require.New(t)
	var testStore = NewStore("/tmp")
	testStore.Config.KeepFilesCount = 1
	should.Nil(testStore.Add([]byte(`{"url":"/hello"}`)))
	testStore.flushInputQueue()
	timeutil.MockNow(timeutil.Now().Add(time.Hour))
	should.Nil(testStore.Add([]byte(`{"url":"/hello"}`)))
	testStore.flushInputQueue()
	testStore.clean()
	dir, _ := fs.ReadDir("/tmp")
	should.Len(dir, 1)
	should.Equal("201701010900", dir[0].Name())
}

func Test_list_skip_and_limit(t *testing.T) {
	reset()
	should := require.New(t)
	var testStore = NewStore("/tmp")
	should.Nil(testStore.Add([]byte(`{"url":"/hello1"}`)))
	testStore.flushInputQueue()
	should.Nil(testStore.Add([]byte(`{"url":"/hello2"}`)))
	testStore.flushInputQueue()
	events, err := testStore.List(timeutil.Now(), timeutil.Now().Add(time.Hour*24), 1, 1)
	should.Nil(err)
	blockId, block, events := events.Next()
	should.Equal("201701010800", blockId.FileName())
	should.Equal(uint64(0x46), blockId.Offset())
	entries := block.EventEntries()
	entry, entries := entries.Next()
	should.Equal(`{"url":"/hello2"}`, string(entry.EventBody()))
}

func Test_list_time_range(t *testing.T) {
	reset()
	should := require.New(t)
	var testStore = NewStore("/tmp")
	today := timeutil.Now()
	yesterday := today.Add(-time.Hour * 24)
	timeutil.MockNow(yesterday)
	testStore.flushInputQueue()
	should.Nil(testStore.Add([]byte(`{"url":"/hello1"}`)))
	timeutil.MockNow(today)
	should.Nil(testStore.Add([]byte(`{"url":"/hello2"}`)))
	testStore.flushInputQueue()
	timeutil.MockNow(today.Add(time.Minute * 2))
	should.Nil(testStore.Add([]byte(`{"url":"/hello3"}`)))
	testStore.flushInputQueue()
	events, err := testStore.List(today, today.Add(time.Minute), 0, 10)
	should.Nil(err)
	blockId, block, events := events.Next()
	should.Equal("201701010800", blockId.FileName())
	should.Equal(uint64(0x19), blockId.Offset())
	entries := block.EventEntries()
	entry, entries := entries.Next()
	should.Equal(`{"url":"/hello2"}`, string(entry.EventBody()))
	should.Len(entries, 0)
}
