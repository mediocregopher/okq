package peel

import (
	. "testing"
	"time"

	"github.com/levenlabs/golib/testutil"
	"github.com/mediocregopher/bananaq/core"
	"github.com/mediocregopher/radix.v2/pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPeel() Peel {
	p, err := pool.New("tcp", "127.0.0.1:6379", 1)
	if err != nil {
		panic(err)
	}

	o := &core.Opts{
		RedisPrefix: testutil.RandStr(),
	}
	peel, err := New(p, o)
	if err != nil {
		panic(err)
	}
	go func() { panic(peel.Run()) }()
	return peel
}

var testPeel = newTestPeel()

func keyElems(t *T, k core.Key) []core.Event {
	qa := core.QueryActions{
		KeyBase: k.Base,
		QueryActions: []core.QueryAction{
			{
				QuerySelector: &core.QuerySelector{
					Key: k,
					QueryEventRangeSelect: &core.QueryEventRangeSelect{},
				},
			},
		},
	}
	ee, err := testPeel.c.Query(qa)
	require.Nil(t, err)
	return ee
}

func assertKey(t *T, k core.Key, ids ...core.ID) {
	ee := keyElems(t, k)
	eeIDs := map[core.ID]bool{}
	for _, e := range ee {
		eeIDs[e.ID] = true
	}
	assert.Len(t, eeIDs, len(ids))
	for _, id := range ids {
		assert.Contains(t, eeIDs, id, "doesn't contain id: %d", id)
	}
}

func TestQAdd(t *T) {
	queue := testutil.RandStr()
	contents := testutil.RandStr()
	id, err := testPeel.QAdd(QAddCommand{
		Queue:    queue,
		Expire:   time.Now().Add(10 * time.Second),
		Contents: contents,
	})
	require.Nil(t, err)
	assert.NotZero(t, id)
	assertKey(t, queueAvailableByID(queue), id)
	assertKey(t, queueAvailableByExpire(queue), id)

	e, err := testPeel.c.GetEvent(id)
	require.Nil(t, err)
	assert.Equal(t, contents, e.Contents)
}

// score is optional
func requireAddToKey(t *T, k core.Key, e core.Event, score core.TS) {
	qa := core.QueryActions{
		KeyBase: k.Base,
		QueryActions: []core.QueryAction{
			{
				QuerySelector: &core.QuerySelector{
					Events: []core.Event{e},
				},
			},
			{
				QueryAddTo: &core.QueryAddTo{
					Keys:  []core.Key{k},
					Score: score,
				},
			},
		},
	}
	_, err := testPeel.c.Query(qa)
	require.Nil(t, err)
}

func newTestQueue(t *T, numEvents int) (string, []core.Event) {
	queue := testutil.RandStr()
	var ee []core.Event
	for i := 0; i < numEvents; i++ {
		id, err := testPeel.QAdd(QAddCommand{
			Queue:    queue,
			Expire:   time.Now().Add(10 * time.Minute),
			Contents: testutil.RandStr(),
		})
		require.Nil(t, err)
		e, err := testPeel.c.GetEvent(id)
		require.Nil(t, err)
		ee = append(ee, e)
	}
	return queue, ee
}

func randEmptyEvent(t *T, expired bool) core.Event {
	id, err := testPeel.c.NewID(core.NewTS(time.Now()))
	require.Nil(t, err)

	var expire core.TS
	if expired {
		expire = core.NewTS(time.Now().Add(-10 * time.Second))
	} else {
		expire = core.NewTS(time.Now().Add(10 * time.Second))
	}
	return core.Event{ID: id, Expire: expire}
}

func TestQGet(t *T) {
	queue, ee := newTestQueue(t, 6)
	cgroup := testutil.RandStr()

	for i, e := range ee {
		t.Logf("ee[%d]: %d (0x%x)", i, e.ID, e.ID)
	}

	keyInProgID := queueInProgressByID(queue, cgroup)
	keyInProgAck := queueInProgressByAck(queue, cgroup)
	keyRedo := queueRedo(queue, cgroup)
	keyDone := queueDone(queue, cgroup)
	keyInUse := queueInUseByExpire(queue, cgroup)

	// Test that a "blank" queue gives us its first event
	cmd := QGetCommand{
		Queue:         queue,
		ConsumerGroup: cgroup,
		AckDeadline:   time.Now().Add(1 * time.Second),
	}
	e, err := testPeel.QGet(cmd)
	require.Nil(t, err)
	assert.Equal(t, ee[0], e)
	assertKey(t, keyInProgID, ee[0].ID)
	assertKey(t, keyInProgAck, ee[0].ID)
	assertKey(t, keyInUse, ee[0].ID)

	// Test that a queue with empty done but an inProg returns one after inProg
	e, err = testPeel.QGet(cmd)
	require.Nil(t, err)
	assert.Equal(t, ee[1], e)
	assertKey(t, keyInProgID, ee[0].ID, ee[1].ID)
	assertKey(t, keyInProgAck, ee[0].ID, ee[1].ID)
	assertKey(t, keyInUse, ee[0].ID, ee[1].ID)

	// Test that empty expire goes straight to done
	cmd.AckDeadline = time.Time{}
	e, err = testPeel.QGet(cmd)
	require.Nil(t, err)
	assert.Equal(t, ee[2], e)
	assertKey(t, keyInProgID, ee[0].ID, ee[1].ID)
	assertKey(t, keyInProgAck, ee[0].ID, ee[1].ID)
	assertKey(t, keyDone, ee[2].ID)

	// Test that a queue with an event in done ahead of all events in inProg
	// returns the next one correctly
	e, err = testPeel.QGet(cmd)
	require.Nil(t, err)
	assert.Equal(t, ee[3], e)
	assertKey(t, keyInProgID, ee[0].ID, ee[1].ID)
	assertKey(t, keyInProgAck, ee[0].ID, ee[1].ID)
	assertKey(t, keyDone, ee[2].ID, ee[3].ID)

	// Artifically add two events to redo, make sure they come out in that order
	// immediately
	requireAddToKey(t, keyRedo, ee[4], 0)
	requireAddToKey(t, keyRedo, ee[5], 0)
	assertKey(t, keyRedo, ee[4].ID, ee[5].ID)

	e, err = testPeel.QGet(cmd)
	require.Nil(t, err)
	assert.Equal(t, ee[4], e)
	assertKey(t, keyInProgID, ee[0].ID, ee[1].ID)
	assertKey(t, keyInProgAck, ee[0].ID, ee[1].ID)
	assertKey(t, keyDone, ee[2].ID, ee[3].ID, ee[4].ID)
	assertKey(t, keyRedo, ee[5].ID)

	e, err = testPeel.QGet(cmd)
	require.Nil(t, err)
	assert.Equal(t, ee[5], e)
	assertKey(t, keyInProgID, ee[0].ID, ee[1].ID)
	assertKey(t, keyInProgAck, ee[0].ID, ee[1].ID)
	assertKey(t, keyDone, ee[2].ID, ee[3].ID, ee[4].ID, ee[5].ID)
	assertKey(t, keyRedo) // assert empty

	// At this point the queue has no available events, make sure empty event is
	// returned
	e, err = testPeel.QGet(cmd)
	require.Nil(t, err)
	assert.Equal(t, core.Event{}, e)
	assertKey(t, keyInProgID, ee[0].ID, ee[1].ID)
	assertKey(t, keyInProgAck, ee[0].ID, ee[1].ID)
	assertKey(t, keyDone, ee[2].ID, ee[3].ID, ee[4].ID, ee[5].ID)
	assertKey(t, keyRedo) // assert empty

	// Now we're gonna do something mean, and insert an event with an expire
	// which is before the most recent expire in done
	contents := testutil.RandStr()
	expire := core.NewTS(ee[5].Expire.Time().Add(-5 * time.Second))
	id, err := testPeel.QAdd(QAddCommand{
		Queue:    queue,
		Expire:   expire.Time(),
		Contents: contents,
	})
	require.Nil(t, err)
	e, err = testPeel.QGet(cmd)
	require.Nil(t, err)
	assert.Equal(t, core.Event{ID: id, Expire: expire, Contents: contents}, e)
	assertKey(t, keyInProgID, ee[0].ID, ee[1].ID)
	assertKey(t, keyInProgAck, ee[0].ID, ee[1].ID)
	assertKey(t, keyDone, ee[2].ID, ee[3].ID, ee[4].ID, id, ee[5].ID)
	assertKey(t, keyRedo) // assert empty
}

func TestQGetBlocking(t *T) {
	queue, ee := newTestQueue(t, 1)
	cgroup := testutil.RandStr()

	cmd := QGetCommand{
		Queue:         queue,
		ConsumerGroup: cgroup,
		BlockUntil:    time.Now().Add(1 * time.Second),
	}

	assertBlockFor := func(d time.Duration) core.Event {
		ch := make(chan core.Event)
		go func() {
			e, err := testPeel.QGet(cmd)
			require.Nil(t, err)
			ch <- e
		}()

		if d > 0 {
			select {
			case <-time.After(d):
			case <-ch:
				assert.Fail(t, "didn't block long enough")
			}
		}
		select {
		case <-time.After(100 * time.Millisecond):
			assert.Fail(t, "blocked too long")
		case e := <-ch:
			return e
		}
		return core.Event{}
	}

	e := assertBlockFor(0)
	assert.Equal(t, ee[0], e)

	cmd.BlockUntil = time.Now().Add(1*time.Second + 10*time.Millisecond)
	e = assertBlockFor(1 * time.Second)
	assert.Equal(t, core.Event{}, e)

	cmd.BlockUntil = time.Now().Add(1 * time.Second)
	e2ch := make(chan core.Event)
	go func() {
		time.Sleep(500 * time.Millisecond)
		contents := testutil.RandStr()
		expire := core.NewTS(time.Now().Add(10 * time.Minute))
		id, err := testPeel.QAdd(QAddCommand{
			Queue:    queue,
			Expire:   expire.Time(),
			Contents: contents,
		})
		require.Nil(t, err)
		e2ch <- core.Event{ID: id, Expire: expire, Contents: contents}
	}()
	e = assertBlockFor(500 * time.Millisecond)
	e2 := <-e2ch
	assert.Equal(t, e2, e)
}

func TestQAck(t *T) {
	queue, ee := newTestQueue(t, 2)
	cgroup := testutil.RandStr()
	keyInProgID := queueInProgressByID(queue, cgroup)
	keyInProgAck := queueInProgressByAck(queue, cgroup)
	keyDone := queueDone(queue, cgroup)

	now := time.Now()
	requireAddToKey(t, keyInProgID, ee[0], 0)
	requireAddToKey(t, keyInProgAck, ee[0], core.NewTS(now.Add(10*time.Millisecond)))

	cmd := QAckCommand{
		Queue:         queue,
		ConsumerGroup: cgroup,
		Event:         ee[0],
	}
	acked, err := testPeel.QAck(cmd)
	require.Nil(t, err)
	assert.True(t, acked)
	assertKey(t, keyInProgID)
	assertKey(t, keyInProgAck)
	assertKey(t, keyDone, ee[0].ID)

	acked, err = testPeel.QAck(cmd)
	require.Nil(t, err)
	assert.False(t, acked)
	assertKey(t, keyInProgID)
	assertKey(t, keyInProgAck)
	assertKey(t, keyDone, ee[0].ID)

	requireAddToKey(t, keyInProgID, ee[1], 0)
	requireAddToKey(t, keyInProgAck, ee[1], core.NewTS(now.Add(-10*time.Millisecond)))

	cmd.Event = ee[1]
	acked, err = testPeel.QAck(cmd)
	require.Nil(t, err)
	assert.False(t, acked)
	assertKey(t, keyInProgID, ee[1].ID)
	assertKey(t, keyInProgAck, ee[1].ID)
	assertKey(t, keyDone, ee[0].ID)
}

func TestClean(t *T) {
	queue := testutil.RandStr()
	cgroup := testutil.RandStr()
	keyInProgID := queueInProgressByID(queue, cgroup)
	keyInProgAck := queueInProgressByAck(queue, cgroup)
	keyDone := queueDone(queue, cgroup)
	keyRedo := queueRedo(queue, cgroup)
	keyInUse := queueInUseByExpire(queue, cgroup)
	now := time.Now()

	// in progress, has neither expired nor missed its deadline
	ee0 := randEmptyEvent(t, false)
	requireAddToKey(t, keyInProgID, ee0, 0)
	requireAddToKey(t, keyInProgAck, ee0, core.NewTS(now.Add(1*time.Second)))
	requireAddToKey(t, keyInUse, ee0, ee0.Expire)

	// in progress, missed its deadline
	ee1 := randEmptyEvent(t, false)
	requireAddToKey(t, keyInProgID, ee1, 0)
	requireAddToKey(t, keyInProgAck, ee1, core.NewTS(now.Add(-10*time.Millisecond)))
	requireAddToKey(t, keyInUse, ee1, ee1.Expire)

	// in progress, expired
	ee2 := randEmptyEvent(t, true)
	requireAddToKey(t, keyInProgID, ee2, 0)
	requireAddToKey(t, keyInProgAck, ee2, core.NewTS(now.Add(-10*time.Millisecond)))
	requireAddToKey(t, keyInUse, ee2, ee2.Expire)

	// in redo, not expired
	ee3 := randEmptyEvent(t, false)
	requireAddToKey(t, keyRedo, ee3, 0)
	requireAddToKey(t, keyInUse, ee3, ee3.Expire)

	// in redo, expired
	ee4 := randEmptyEvent(t, true)
	requireAddToKey(t, keyRedo, ee4, 0)
	requireAddToKey(t, keyInUse, ee4, ee4.Expire)

	// in done, not expired
	ee5 := randEmptyEvent(t, false)
	requireAddToKey(t, keyDone, ee5, 0)
	requireAddToKey(t, keyInUse, ee5, ee5.Expire)

	// in done, expired
	ee6 := randEmptyEvent(t, true)
	requireAddToKey(t, keyDone, ee6, 0)
	requireAddToKey(t, keyInUse, ee6, ee6.Expire)

	require.Nil(t, testPeel.Clean(queue, cgroup))
	assertKey(t, keyInProgID, ee0.ID)
	assertKey(t, keyInProgAck, ee0.ID)
	assertKey(t, keyDone, ee5.ID)
	assertKey(t, keyRedo, ee1.ID, ee3.ID)
	assertKey(t, keyInUse, ee0.ID, ee1.ID, ee3.ID, ee5.ID)
}

func TestCleanAvailable(t *T) {
	queue := testutil.RandStr()
	keyAvailID := queueAvailableByID(queue)
	keyAvailEx := queueAvailableByExpire(queue)

	ee0 := randEmptyEvent(t, false)
	requireAddToKey(t, keyAvailID, ee0, 0)
	requireAddToKey(t, keyAvailEx, ee0, ee0.Expire)
	ee1 := randEmptyEvent(t, true)
	requireAddToKey(t, keyAvailID, ee1, 0)
	requireAddToKey(t, keyAvailEx, ee1, ee1.Expire)
	ee2 := randEmptyEvent(t, false)
	requireAddToKey(t, keyAvailID, ee2, 0)
	requireAddToKey(t, keyAvailEx, ee2, ee2.Expire)
	ee3 := randEmptyEvent(t, true)
	requireAddToKey(t, keyAvailID, ee3, 0)
	requireAddToKey(t, keyAvailEx, ee3, ee3.Expire)

	require.Nil(t, testPeel.CleanAvailable(queue))
	assertKey(t, keyAvailID, ee0.ID, ee2.ID)
}

func TestQStatus(t *T) {
	queue, ee := newTestQueue(t, 6)
	cgroup := testutil.RandStr()
	keyInProgID := queueInProgressByID(queue, cgroup)
	keyDone := queueDone(queue, cgroup)
	keyRedo := queueRedo(queue, cgroup)

	requireAddToKey(t, keyInProgID, ee[0], 0)
	requireAddToKey(t, keyDone, ee[1], 0)
	requireAddToKey(t, keyDone, ee[2], 0)
	requireAddToKey(t, keyRedo, ee[3], 0)

	qs, err := testPeel.QStatus(QStatusCommand{
		Queues:         []string{queue},
		ConsumerGroups: []string{cgroup},
	})
	require.Nil(t, err)

	expected := map[string]QueueStats{
		queue: QueueStats{
			Total: 6,
			ConsumerGroupStats: map[string]ConsumerGroupStats{
				cgroup: ConsumerGroupStats{
					InProgress: 1,
					Done:       2,
					Redo:       1,
					Available:  2,
				},
			},
		},
	}

	assert.Equal(t, expected, qs)
}
