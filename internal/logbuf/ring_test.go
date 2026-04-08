package logbuf

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRing_WriteAndRead(t *testing.T) {
	r := New(5)

	r.Write("line1")
	r.Write("line2")
	r.Write("line3")

	lines := r.Lines()
	require.Len(t, lines, 3)
	assert.Equal(t, "line1", lines[0].Text)
	assert.Equal(t, "line2", lines[1].Text)
	assert.Equal(t, "line3", lines[2].Text)
}

func TestRing_Overflow(t *testing.T) {
	r := New(3)

	r.Write("a")
	r.Write("b")
	r.Write("c")
	r.Write("d") // overwrites "a"
	r.Write("e") // overwrites "b"

	lines := r.Lines()
	require.Len(t, lines, 3)
	assert.Equal(t, "c", lines[0].Text)
	assert.Equal(t, "d", lines[1].Text)
	assert.Equal(t, "e", lines[2].Text)
}

func TestRing_Empty(t *testing.T) {
	r := New(10)
	assert.Nil(t, r.Lines())
	assert.Equal(t, 0, r.Len())
}

func TestRing_Subscribe(t *testing.T) {
	r := New(100)
	ch := r.Subscribe()
	defer r.Unsubscribe(ch)

	r.Write("hello")

	select {
	case line := <-ch:
		assert.Equal(t, "hello", line.Text)
		assert.False(t, line.Timestamp.IsZero())
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive line")
	}
}

func TestRing_MultipleSubscribers(t *testing.T) {
	r := New(100)
	ch1 := r.Subscribe()
	ch2 := r.Subscribe()
	defer r.Unsubscribe(ch1)
	defer r.Unsubscribe(ch2)

	r.Write("broadcast")

	for _, ch := range []chan Line{ch1, ch2} {
		select {
		case line := <-ch:
			assert.Equal(t, "broadcast", line.Text)
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive line")
		}
	}
}

func TestRing_UnsubscribeRemovesFromMap(t *testing.T) {
	r := New(10)
	ch := r.Subscribe()
	r.Unsubscribe(ch)

	// After unsubscribe, Write should not send to this channel.
	r.Write("after unsubscribe")

	select {
	case <-ch:
		t.Fatal("unsubscribed channel should not receive new lines")
	default:
		// Expected: channel is empty because subscriber was removed.
	}
}

func TestRing_ConcurrentWriteUnsubscribe(t *testing.T) {
	// Regression test for F-1: Write + Unsubscribe must not panic
	// (send on closed channel). Run with -race to verify.
	r := New(100)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		ch := r.Subscribe()
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Write("concurrent line")
			}
		}()
		go func() {
			defer wg.Done()
			// Unsubscribe while writes are in flight.
			r.Unsubscribe(ch)
		}()
	}
	wg.Wait()
}

func TestRing_ConcurrentWrites(t *testing.T) {
	r := New(100)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Write("concurrent")
		}()
	}
	wg.Wait()
	assert.Equal(t, 50, r.Len())
}

func TestRing_DefaultCapacity(t *testing.T) {
	r := New(0)
	assert.Equal(t, DefaultCapacity, r.cap)
}
