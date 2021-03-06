// +build linux

package fsutil

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/docker/docker/builder"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

type hashed interface {
	Hash() string
}

func TestCopySimple(t *testing.T) {
	d, err := tmpDir(changeStream([]string{
		"ADD foo file data1",
		"ADD foo2 file dat2",
		"ADD zzz dir",
		"ADD zzz/aa file data3",
		"ADD zzz/bb dir",
		"ADD zzz/bb/cc dir",
		"ADD zzz/bb/cc/dd symlink ../../",
	}))
	assert.NoError(t, err)
	defer os.RemoveAll(d)

	dest, err := ioutil.TempDir("", "dest")
	assert.NoError(t, err)
	defer os.RemoveAll(dest)

	s1, s2 := sockPairProto()

	ts := NewTarsum("")

	var err1 error
	var err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		err1 = Send(context.Background(), s1, d, nil, nil)
		wg.Done()
	}()
	go func() {
		err2 = Receive(context.Background(), s2, dest, ts.HandleChange)
		wg.Done()
	}()

	wg.Wait()
	assert.NoError(t, err1)
	assert.NoError(t, err2)

	b := &bytes.Buffer{}
	err = Walk(context.Background(), dest, nil, bufWalk(b))
	assert.NoError(t, err)

	assert.Equal(t, string(b.Bytes()), `file foo
file foo2
dir zzz
file zzz/aa
dir zzz/bb
dir zzz/bb/cc
symlink:../../ zzz/bb/cc/dd
`)

	dt, err := ioutil.ReadFile(filepath.Join(dest, "zzz/aa"))
	assert.NoError(t, err)
	assert.Equal(t, "data3", string(dt))

	dt, err = ioutil.ReadFile(filepath.Join(dest, "foo2"))
	assert.NoError(t, err)
	assert.Equal(t, "dat2", string(dt))

	_, fi, err := ts.Stat("zzz/aa")
	assert.NoError(t, err)
	assert.Equal(t, string(fi.(Hashed).Hash()), "ef92a0ceed897e1accac62fce380aa5d953d55c90a5b643e5b59c0b686b9e076")

	_, fi, err = ts.Stat("foo2")
	assert.NoError(t, err)
	assert.Equal(t, string(fi.(Hashed).Hash()), "4524c0852a5745ea830e63da563f58e6b507ca1bfdf0075db3baa627317651cb")

	_, fi, err = ts.Stat("zzz/bb/cc/dd")
	assert.NoError(t, err)
	assert.Equal(t, string(fi.(Hashed).Hash()), "19115809186a0dcabef79252d37f44bdd0e29d95e289d0493e79c6d5d6b2ef84")

	c := &counter{}
	err = ts.Walk("zzz/bb", c.inc)
	assert.NoError(t, err)
	assert.Equal(t, c.c, 3)

	err = ioutil.WriteFile(filepath.Join(d, "zzz/bb/cc/foo"), []byte("data5"), 0600)
	assert.NoError(t, err)

	err = os.RemoveAll(filepath.Join(d, "foo2"))
	assert.NoError(t, err)

	wg.Add(2)
	go func() {
		err1 = Send(context.Background(), s1, d, nil, nil)
		wg.Done()
	}()
	go func() {
		err2 = Receive(context.Background(), s2, dest, ts.HandleChange)
		wg.Done()
	}()

	wg.Wait()
	assert.NoError(t, err1)
	assert.NoError(t, err2)

	b = &bytes.Buffer{}
	err = Walk(context.Background(), dest, nil, bufWalk(b))
	assert.NoError(t, err)

	assert.Equal(t, string(b.Bytes()), `file foo
dir zzz
file zzz/aa
dir zzz/bb
dir zzz/bb/cc
symlink:../../ zzz/bb/cc/dd
file zzz/bb/cc/foo
`)

	dt, err = ioutil.ReadFile(filepath.Join(dest, "zzz/bb/cc/foo"))
	assert.NoError(t, err)
	assert.Equal(t, "data5", string(dt))

	_, fi, err = ts.Stat("zzz/bb/cc/dd")
	assert.NoError(t, err)
	assert.Equal(t, string(fi.(Hashed).Hash()), "19115809186a0dcabef79252d37f44bdd0e29d95e289d0493e79c6d5d6b2ef84")

	_, fi, err = ts.Stat("zzz/bb/cc/foo")
	assert.NoError(t, err)
	assert.Equal(t, string(fi.(Hashed).Hash()), "cfc52c1ad4acdf20b04a90edad8ceb67f9e9ce8c31aaf565e97a845e62e7152c")

	_, fi, err = ts.Stat("foo2")
	assert.Error(t, err)

	c = &counter{}
	err = ts.Walk("zzz/bb", c.inc)
	assert.NoError(t, err)
	assert.Equal(t, c.c, 4)

	c = &counter{}
	err = ts.Walk("zzz/bb/cc/dd", c.inc)
	assert.NoError(t, err)
	assert.Equal(t, c.c, 1)
}

func sockPair() (Stream, Stream) {
	c1 := make(chan *Packet, 32)
	c2 := make(chan *Packet, 32)
	return &fakeConn{c1, c2}, &fakeConn{c2, c1}
}

func sockPairProto() (Stream, Stream) {
	c1 := make(chan []byte, 32)
	c2 := make(chan []byte, 32)
	return &fakeConnProto{c1, c2}, &fakeConnProto{c2, c1}
}

type fakeConn struct {
	recvChan chan *Packet
	sendChan chan *Packet
}

func (fc *fakeConn) RecvMsg(m interface{}) error {
	p, ok := m.(*Packet)
	if !ok {
		return errors.Errorf("invalid msg: %#v", m)
	}
	p2 := <-fc.recvChan
	*p = *p2
	return nil
}

func (fc *fakeConn) SendMsg(m interface{}) error {
	p, ok := m.(*Packet)
	if !ok {
		return errors.Errorf("invalid msg: %#v", m)
	}
	p2 := *p
	p2.Data = append([]byte{}, p2.Data...)
	fc.sendChan <- &p2
	return nil
}

type fakeConnProto struct {
	recvChan chan []byte
	sendChan chan []byte
}

func (fc *fakeConnProto) RecvMsg(m interface{}) error {
	p, ok := m.(*Packet)
	if !ok {
		return errors.Errorf("invalid msg: %#v", m)
	}
	dt := <-fc.recvChan
	return p.Unmarshal(dt)
}

func (fc *fakeConnProto) SendMsg(m interface{}) error {
	p, ok := m.(*Packet)
	if !ok {
		return errors.Errorf("invalid msg: %#v", m)
	}
	dt, err := p.Marshal()
	if err != nil {
		return err
	}
	fc.sendChan <- dt
	return nil
}

type counter struct {
	c int
}

func (c *counter) inc(p string, fi builder.FileInfo, err error) error {
	c.c++
	return err
}
