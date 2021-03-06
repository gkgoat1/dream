package engine

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/robertkrimen/otto"
	"github.com/robertkrimen/otto/registry"
	_ "github.com/robertkrimen/otto/underscore"
	"golang.org/x/exp/slices"
)

//go:embed utils.js
var utils string

var entryForUtils *registry.Entry = registry.Register(func() string { return utils })

type Target struct {
	Name    string
	Content []byte
	Done    chan bool
	VM      *otto.Otto
}

func DependOn(m map[string]*Target, t_ string, h chan string, in string) *Target {
	var t string
	if strings.HasPrefix(t_, "@") {
		t = strings.TrimPrefix(t_, "@")
	} else {
		s := strings.Split(in, "//")
		t = strings.Join(s[:len(s)-2], "//") + ":" + strings.TrimPrefix(t_, "//")
	}
	go func() {
		h <- t
	}()
	var tt *Target
	for {
		if m[t] != nil {
			tt = m[t]
			goto l1
		}
	}
l1:
	x := <-tt.Done
	go func() {
		tt.Done <- x
	}()
	return tt
}

func HashSlice(sl []string, sum []byte) []byte {
	s := sha256.New()
	sl2 := slices.Clone(sl)
	slices.Sort(sl2)
	for _, s2 := range sl2 {
		s.Write([]byte(s2))
	}
	return s.Sum(sum)
}

type Cache struct {
	Local      map[string]map[string][]byte
	FileFolder *string
	Lock       sync.Mutex
}

func (c *Cache) Activate(hs string) {
	if c.Local[hs] != nil {
		return
	}
	if c.FileFolder != nil {
		var t map[string][]byte
		f, err := os.Open(*c.FileFolder + "/" + hs)
		if err == nil {
			defer f.Close()
			g, _ := gzip.NewReader(f)
			if json.NewDecoder(g).Decode(t) == nil {
				c.Local[hs] = t
				return
			}
		}
	}
}

func (c *Cache) Get(hs, k string) ([]byte, bool) {
	c.Lock.Lock()
	defer c.Lock.Unlock()
	c.Activate(hs)
	a, b := c.Local[hs][k]
	return a, b
}

func (c *Cache) Sync() {
	c.Lock.Lock()
	defer c.Lock.Unlock()
	h := make(chan bool)
	toSync := 0
	for k, l := range c.Local {
		if _, err := os.Stat(*c.FileFolder + "/" + k); errors.Is(err, os.ErrNotExist) {
			toSync += 1
			go func() {
				defer func() { h <- true }()
				f, _ := os.Create(*c.FileFolder + "/" + k)
				defer f.Close()
				json.NewEncoder(gzip.NewWriter(f)).Encode(l)
			}()
		}
	}

	for i := 0; i < toSync; i++ {
		<-h
	}
}

func SetupVM(v *otto.Otto, m map[string]*Target, b string, h chan string, cache *Cache, proc chan bool, idx chan *string) {
	v.Set("dependOn", func(call otto.FunctionCall) otto.Value {
		rx := make([][]byte, len(call.ArgumentList))
		c := make(chan bool)
		for i, a := range call.ArgumentList {
			i := i
			a := a
			go func() {
				s, _ := a.ToString()
				if strings.HasPrefix(s, ":") {
					s = InjectTarget(b, s)
				}
				u := DependOn(m, s, h, b).Content
				rx[i] = u
				c <- true
			}()
		}
		for range call.ArgumentList {
			<-c
		}
		r, _ := v.ToValue(rx)
		return r
	})
	v.Set("dependOnS", func(call otto.FunctionCall) otto.Value {
		rx := make([]string, len(call.ArgumentList))
		c := make(chan bool)
		for i, a := range call.ArgumentList {
			i := i
			a := a
			go func() {
				s, _ := a.ToString()
				if strings.HasPrefix(s, ":") {
					s = InjectTarget(b, s)
				}
				u := DependOn(m, s, h, b).Content
				rx[i] = string(u)
				c <- true
			}()
		}
		for range call.ArgumentList {
			<-c
		}
		r, _ := v.ToValue(rx)
		return r
	})
	v.Set("exec", func(call otto.FunctionCall) otto.Value {
		h := sha256.New()
		gob.NewEncoder(h).Encode(call)
		hs := base64.StdEncoding.EncodeToString(h.Sum([]byte("dream!action")))
		p := make(map[string][]byte)
		defer func() {
			cache.Lock.Lock()
			defer cache.Lock.Unlock()
			cache.Local[hs] = p
		}()
		cache.Lock.Lock()
		if cache.Local[hs] != nil {
			p = cache.Local[hs]
			cache.Lock.Unlock()
		} else {
			cache.Lock.Unlock()
			proc <- true
			id := <-idx
			defer func() { <-proc; idx <- id }()
			*id = fmt.Sprintf("Build %s:%s", b, hs)
			cmd, _ := call.Argument(0).ToString()
			sd, _ := os.MkdirTemp(os.TempDir(), "dream-**")
			defer os.RemoveAll(sd)
			c := exec.Command("sh", "-c", cmd)
			c.Dir = sd
			o := call.Argument(1).Object()
			for _, k := range o.Keys() {
				v, _ := o.Get(k)
				y, _ := v.Export()
				ioutil.WriteFile(sd+k, y.([]byte), 0o777)
			}
			c.Run()
			x, _ := call.Argument(1).Export()
			z := x.([]string)
			for _, w := range z {
				p[w], _ = ioutil.ReadFile(sd + w)
			}
		}
		r, _ := v.ToValue(p)
		return r
	})
	v.Set("makeGob", func(call otto.FunctionCall) otto.Value {
		x, _ := call.Argument(0).Export()
		var s bytes.Buffer
		gob.NewEncoder(bufio.NewWriter(&s)).Encode(x)
		r, _ := v.ToValue(s.Bytes())
		return r
	})
	v.Set("extractGob", func(call otto.FunctionCall) otto.Value {
		x, _ := call.Argument(0).Export()
		s := bytes.NewBuffer(x.([]byte))
		var i interface{}
		gob.NewDecoder(s).Decode(i)
		r, _ := v.ToValue(i)
		return r
	})
	v.Set("barray2string", func(call otto.FunctionCall) otto.Value {
		x, _ := call.Argument(0).Export()
		y, _ := v.ToValue(string(x.([]byte)))
		return y
	})
}
func BuildFile(x string) string {
	s := strings.Split(x, ":")
	return strings.Join(s[:len(s)-2], ":") + "/DREAM"
}
func InjectTarget(x, y string) string {
	s := strings.Split(x, "/")
	return strings.Join(s[:len(s)-2], "/") + y
}
func Build(m map[string]*Target, x string, h chan string, cache *Cache, proc chan bool, idx chan *string) {
	if tt, ok := m[x]; ok {
		x := <-tt.Done
		go func() {
			tt.Done <- x
		}()
		return
	}
	b := DependOn(m, BuildFile(x), h, BuildFile(BuildFile(x)))
	hash := sha256.New()
	hash.Write(b.Content)
	hash.Write([]byte(x))
	hs := base64.StdEncoding.EncodeToString(hash.Sum([]byte("dream!build")))
	defer func() {
		go func() {
			m[x].Done <- true
		}()
	}()
	if k, ok := cache.Get(hs, "#Main"); ok {
		m[x] = &Target{Done: make(chan bool), Name: x, Content: k}
		return
	}
	defer func() {
		cache.Lock.Lock()
		defer cache.Lock.Unlock()
		cache.Local[hs]["#Main"] = m[x].Content
	}()
	if b.VM == nil {
		v := otto.New()
		SetupVM(v, m, b.Name, h, cache, proc, idx)
		b.VM = v
	}
	g, _ := b.VM.Get("Build")
	r, _ := g.Call(b.VM.ToValue(x))
	y, _ := r.Export()
	m[x] = &Target{Done: make(chan bool), Name: x, Content: y.([]byte)}
}

func BuildLoop(m map[string]*Target, h chan string, cache *Cache, proc chan bool, idx chan *string) {
	for {
		w := <-h
		Build(m, w, h, cache, proc, idx)
	}
}
