package main

import (
	"bytes"
	"container/ring"
	"io"
	"log"
	"net"
	"net/textproto"
	"sort"
	"strconv"
	"strings"

	"github.com/dustin/go-nntp"
	"github.com/dustin/go-nntp/server"
)

const maxArticles = 100

type articleRef struct {
	msgid string
	num   int64
}

type groupStorage struct {
	group *nntp.Group
	// article refs
	articles *ring.Ring
}

type articleStorage struct {
	headers  textproto.MIMEHeader
	body     string
	refcount int
}

type testBackendType struct {
	// group name -> group storage
	groups map[string]*groupStorage
	// message ID -> article
	articles map[string]*articleStorage
}

var testBackend = testBackendType{
	groups:   make(map[string]*groupStorage),
	articles: make(map[string]*articleStorage),
}

func init() {

	testBackend.groups["alt.test"] = &groupStorage{
		group: &nntp.Group{"alt.test", "A test.",
			0, 0, 0, nntp.PostingNotPermitted},
		articles: ring.New(maxArticles),
	}

	testBackend.groups["misc.test"] = &groupStorage{
		group: &nntp.Group{"misc.test", "More testing.",
			0, 0, 0, nntp.PostingPermitted},
		articles: ring.New(maxArticles),
	}

}

func (tb *testBackendType) ListGroups(max int) ([]*nntp.Group, error) {
	rv := make([]*nntp.Group, 0, 100)
	for _, g := range tb.groups {
		rv = append(rv, g.group)
	}
	return rv, nil
}

func (tb *testBackendType) GetGroup(name string) (*nntp.Group, error) {
	var group *nntp.Group

	for _, g := range tb.groups {
		if g.group.Name == name {
			group = g.group
			break
		}
	}

	if group == nil {
		return nil, nntpserver.NoSuchGroup
	}

	return group, nil
}

func mkArticle(a *articleStorage) *nntp.Article {
	return &nntp.Article{
		Header: a.headers,
		Body:   strings.NewReader(a.body),
		Bytes:  len(a.body),
		Lines:  strings.Count(a.body, "\n"),
	}
}

func findInRing(in *ring.Ring, f func(r interface{}) bool) *ring.Ring {
	if f(in.Value) {
		return in
	}
	for p := in.Next(); p != in; p = p.Next() {
		if f(p.Value) {
			return p
		}
	}
	return nil
}

func (tb *testBackendType) GetArticle(group *nntp.Group, id string) (*nntp.Article, error) {

	msgId := id
	var a *articleStorage

	if intid, err := strconv.ParseInt(id, 10, 64); err == nil {
		msgId = ""
		// by int ID.  Gotta go find it.
		if groupStorage, ok := tb.groups[group.Name]; ok {
			r := findInRing(groupStorage.articles, func(v interface{}) bool {
				if v != nil {
					log.Printf("Looking at %v", v)
				}
				if aref, ok := v.(articleRef); ok && aref.num == intid {
					return true
				}
				return false
			})
			if aref, ok := r.Value.(articleRef); ok {
				msgId = aref.msgid
			}
		}
	}

	a = tb.articles[msgId]
	if a == nil {
		return nil, nntpserver.InvalidMessageId
	}

	return mkArticle(a), nil
}

// Because I suck at ring, I'm going to just post-sort these.
type nalist []nntpserver.NumberedArticle

func (n nalist) Len() int {
	return len(n)
}

func (n nalist) Less(i, j int) bool {
	return n[i].Num < n[j].Num
}

func (n nalist) Swap(i, j int) {
	n[i], n[j] = n[j], n[i]
}

func (tb *testBackendType) GetArticles(group *nntp.Group,
	from, to int64) ([]nntpserver.NumberedArticle, error) {

	gs, ok := tb.groups[group.Name]
	if !ok {
		return nil, nntpserver.NoSuchGroup
	}

	log.Printf("Getting articles from %d to %d", from, to)

	rv := make([]nntpserver.NumberedArticle, 0, maxArticles)
	gs.articles.Do(func(v interface{}) {
		if v != nil {
			if aref, ok := v.(articleRef); ok {
				if aref.num >= from && aref.num <= to {
					a, ok := tb.articles[aref.msgid]
					if ok {
						article := mkArticle(a)
						rv = append(rv, nntpserver.NumberedArticle{aref.num, article})
					}
				}
			}
		}
	})

	sort.Sort(nalist(rv))

	return rv, nil
}

func (tb *testBackendType) AllowPost() bool {
	return true
}

func (tb *testBackendType) decr(msgid string) {
	if a, ok := tb.articles[msgid]; ok {
		a.refcount--
		if a.refcount == 0 {
			log.Printf("Getting rid of %v", msgid)
			delete(tb.articles, msgid)
		}
	}
}

func (tb *testBackendType) Post(article *nntp.Article) error {
	log.Printf("Got headers: %#v", article.Header)
	b := []byte{}
	buf := bytes.NewBuffer(b)
	n, err := io.Copy(buf, article.Body)
	if err != nil {
		return err
	}
	log.Printf("Read %d bytes of body", n)

	a := articleStorage{
		headers:  article.Header,
		body:     buf.String(),
		refcount: 0,
	}

	msgId := a.headers.Get("Message-Id")

	if _, ok := tb.articles[msgId]; ok {
		return nntpserver.PostingFailed
	}

	for _, g := range article.Header["Newsgroups"] {
		if g, ok := tb.groups[g]; ok {
			g.articles = g.articles.Next()
			if g.articles.Value != nil {
				aref := g.articles.Value.(articleRef)
				tb.decr(aref.msgid)
			}
			if g.articles.Value != nil || g.group.Low == 0 {
				g.group.Low++
			}
			g.group.High++
			g.articles.Value = articleRef{
				msgId,
				g.group.High,
			}
			log.Printf("Placed %v", g.articles.Value)
			a.refcount++
			g.group.Count = int64(g.articles.Len())

			log.Printf("Stored %v in %v", msgId, g.group.Name)
		}
	}

	if a.refcount > 0 {
		tb.articles[msgId] = &a
	} else {
		return nntpserver.PostingFailed
	}

	return nil
}

func (tb *testBackendType) Authorized() bool {
	return true
}

func (tb *testBackendType) Authenticate(user, pass string) (nntpserver.Backend, error) {
	return nil, nntpserver.AuthRejected
}

func maybefatal(err error, f string, a ...interface{}) {
	if err != nil {
		log.Fatalf(f, a...)
	}
}

func main() {
	a, err := net.ResolveTCPAddr("tcp", ":1119")
	maybefatal(err, "Error resolving listener: %v", err)
	l, err := net.ListenTCP("tcp", a)
	maybefatal(err, "Error setting up listener: %v", err)
	defer l.Close()

	s := nntpserver.NewServer(&testBackend)

	for {
		c, err := l.AcceptTCP()
		maybefatal(err, "Error accepting connection: %v", err)
		go s.Process(c)
	}
}
