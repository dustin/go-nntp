package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dustin/go-nntp"
	"github.com/dustin/go-nntp/server"

	"code.google.com/p/dsallings-couch-go"
)

var groupCacheTimeout = flag.Int("groupTimeout", 60,
	"Time (in seconds), group cache is valid")
var optimisticPost = flag.Bool("optimistic", false,
	"Optimistically return success on store before storing")

type GroupRow struct {
	Group string        `json:"key"`
	Value []interface{} `json:"value"`
}

type GroupResults struct {
	Rows []GroupRow
}

type Attachment struct {
	Type string `json:"content-type"`
	Data []byte `json:"data"`
}

func removeSpace(r rune) rune {
	if r == ' ' || r == '\n' || r == '\r' {
		return -1
	}
	return r
}

func (a *Attachment) MarshalJSON() ([]byte, error) {
	m := map[string]string{
		"content_type": a.Type,
		"data":         strings.Map(removeSpace, base64.StdEncoding.EncodeToString(a.Data)),
	}
	return json.Marshal(m)
}

type Article struct {
	MsgId       string                 `json:"_id"`
	DocType     string                 `json:"type"`
	Headers     map[string][]string    `json:"headers"`
	Bytes       int                    `json:"bytes"`
	Lines       int                    `json:"lines"`
	Nums        map[string]int64       `json:"nums"`
	Attachments map[string]*Attachment `json:"_attachments"`
	Added       time.Time              `json:"added"`
}

type ArticleResults struct {
	Rows []struct {
		Key     []interface{} `json:"key"`
		Article Article       `json:"doc"`
	}
}

type couchBackend struct {
	db        *couch.Database
	groups    map[string]*nntp.Group
	grouplock sync.Mutex
}

func (cb *couchBackend) clearGroups() {
	cb.grouplock.Lock()
	defer cb.grouplock.Unlock()

	log.Printf("Dumping group cache")
	cb.groups = nil
}

func (cb *couchBackend) fetchGroups() error {
	cb.grouplock.Lock()
	defer cb.grouplock.Unlock()

	if cb.groups != nil {
		return nil
	}

	log.Printf("Filling group cache")

	results := GroupResults{}
	err := cb.db.Query("_design/groups/_view/list", map[string]interface{}{
		"group": true,
	}, &results)
	if err != nil {
		return err
	}
	cb.groups = make(map[string]*nntp.Group)
	for _, gr := range results.Rows {
		if gr.Value[0].(string) != "" {
			group := nntp.Group{
				Name:        gr.Group,
				Description: gr.Value[0].(string),
				Count:       int64(gr.Value[1].(float64)),
				Low:         int64(gr.Value[2].(float64)),
				High:        int64(gr.Value[3].(float64)),
				Posting:     nntp.PostingPermitted,
			}
			cb.groups[group.Name] = &group
		}
	}

	go func() {
		time.Sleep(time.Duration(*groupCacheTimeout) * time.Second)
		cb.clearGroups()
	}()

	return nil
}

func (cb *couchBackend) ListGroups(max int) ([]*nntp.Group, error) {
	if cb.groups == nil {
		if err := cb.fetchGroups(); err != nil {
			return nil, err
		}
	}
	rv := make([]*nntp.Group, 0, len(cb.groups))
	for _, g := range cb.groups {
		rv = append(rv, g)
	}
	return rv, nil
}

func (cb *couchBackend) GetGroup(name string) (*nntp.Group, error) {
	if cb.groups == nil {
		if err := cb.fetchGroups(); err != nil {
			return nil, err
		}
	}
	g, exists := cb.groups[name]
	if !exists {
		return nil, nntpserver.NoSuchGroup
	}
	return g, nil
}

func (cb *couchBackend) mkArticle(ar Article) *nntp.Article {
	url := fmt.Sprintf("%s/%s/article", cb.db.DBURL(), cleanupId(ar.MsgId))
	return &nntp.Article{
		Header: textproto.MIMEHeader(ar.Headers),
		Body:   &lazyOpener{url, nil, nil},
		Bytes:  ar.Bytes,
		Lines:  ar.Lines,
	}
}

func (cb *couchBackend) GetArticle(group *nntp.Group, id string) (*nntp.Article, error) {
	var ar Article
	if intid, err := strconv.ParseInt(id, 10, 64); err == nil {
		results := ArticleResults{}
		cb.db.Query("_design/articles/_view/list", map[string]interface{}{
			"include_docs": true,
			"reduce":       false,
			"key":          []interface{}{group.Name, intid},
		}, &results)

		if len(results.Rows) != 1 {
			return nil, nntpserver.InvalidArticleNumber
		}

		ar = results.Rows[0].Article
	} else {
		err := cb.db.Retrieve(cleanupId(id), &ar)
		if err != nil {
			return nil, nntpserver.InvalidMessageId
		}
	}

	return cb.mkArticle(ar), nil
}

func (cb *couchBackend) GetArticles(group *nntp.Group,
	from, to int64) ([]nntpserver.NumberedArticle, error) {

	rv := make([]nntpserver.NumberedArticle, 0, 100)

	results := ArticleResults{}
	cb.db.Query("_design/articles/_view/list", map[string]interface{}{
		"include_docs": true,
		"reduce":       false,
		"start_key":    []interface{}{group.Name, from},
		"end_key":      []interface{}{group.Name, to},
	}, &results)

	for _, r := range results.Rows {
		rv = append(rv, nntpserver.NumberedArticle{
			Num:     int64(r.Key[1].(float64)),
			Article: cb.mkArticle(r.Article),
		})
	}

	return rv, nil
}

func (tb *couchBackend) AllowPost() bool {
	return true
}

func cleanupId(msgid string) string {
	s := strings.TrimFunc(msgid, func(r rune) bool {
		return r == ' ' || r == '<' || r == '>'
	})
	return url.QueryEscape(s)
}

func (cb *couchBackend) Post(article *nntp.Article) error {
	a := Article{
		DocType:     "article",
		Headers:     map[string][]string(article.Header),
		Nums:        make(map[string]int64),
		MsgId:       cleanupId(article.Header.Get("Message-Id")),
		Attachments: make(map[string]*Attachment),
		Added:       time.Now(),
	}

	b := []byte{}
	buf := bytes.NewBuffer(b)
	n, err := io.Copy(buf, article.Body)
	if err != nil {
		return err
	}
	log.Printf("Read %d bytes of body", n)

	b = buf.Bytes()
	a.Bytes = len(b)
	a.Lines = bytes.Count(b, []byte{'\n'})

	a.Attachments["article"] = &Attachment{"text/plain", b}

	for _, g := range strings.Split(article.Header.Get("Newsgroups"), ",") {
		g = strings.TrimSpace(g)
		group, err := cb.GetGroup(g)
		if err == nil {
			a.Nums[g] = atomic.AddInt64(&group.High, 1)
			atomic.AddInt64(&group.Count, 1)
		} else {
			log.Printf("Error getting group %q:  %v", g, err)
		}
	}

	if len(a.Nums) == 0 {
		log.Printf("Found no matching groups in %v",
			article.Header["Newsgroups"])
		return nntpserver.PostingFailed
	}

	if *optimisticPost {
		go func() {
			_, _, err = cb.db.Insert(&a)
			if err != nil {
				log.Printf("error optimistically posting article: %v", err)
			}
		}()
	} else {
		_, _, err = cb.db.Insert(&a)
		if err != nil {
			log.Printf("error posting article: %v", err)
			return nntpserver.PostingFailed
		}
	}

	return nil
}

func (tb *couchBackend) Authorized() bool {
	return true
}

func (tb *couchBackend) Authenticate(user, pass string) (nntpserver.Backend, error) {
	return nil, nntpserver.AuthRejected
}

func maybefatal(err error, f string, a ...interface{}) {
	if err != nil {
		log.Fatalf(f, a...)
	}
}

func main() {

	couchUrl := flag.String("couch", "http://localhost:5984/news",
		"Couch DB.")

	flag.Parse()

	a, err := net.ResolveTCPAddr("tcp", ":1119")
	maybefatal(err, "Error resolving listener: %v", err)
	l, err := net.ListenTCP("tcp", a)
	maybefatal(err, "Error setting up listener: %v", err)
	defer l.Close()

	db, err := couch.Connect(*couchUrl)
	maybefatal(err, "Can't connect to the couch: %v", err)
	err = ensureViews(&db)
	maybefatal(err, "Error setting up views: %v", err)

	backend := couchBackend{
		db: &db,
	}

	s := nntpserver.NewServer(&backend)

	for {
		c, err := l.AcceptTCP()
		maybefatal(err, "Error accepting connection: %v", err)
		go s.Process(c)
	}
}
