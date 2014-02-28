package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/syslog"
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

	"github.com/dustin/go-couch"
)

var groupCacheTimeout = flag.Int("groupTimeout", 300,
	"Time (in seconds), group cache is valid")
var optimisticPost = flag.Bool("optimistic", false,
	"Optimistically return success on store before storing")
var useSyslog = flag.Bool("syslog", false,
	"Log to syslog")

type groupRow struct {
	Group string        `json:"key"`
	Value []interface{} `json:"value"`
}

type groupResults struct {
	Rows []groupRow
}

type attachment struct {
	Type string `json:"content-type"`
	Data []byte `json:"data"`
}

func removeSpace(r rune) rune {
	if r == ' ' || r == '\n' || r == '\r' {
		return -1
	}
	return r
}

func (a *attachment) MarshalJSON() ([]byte, error) {
	m := map[string]string{
		"content_type": a.Type,
		"data":         strings.Map(removeSpace, base64.StdEncoding.EncodeToString(a.Data)),
	}
	return json.Marshal(m)
}

type article struct {
	MsgID       string                 `json:"_id"`
	DocType     string                 `json:"type"`
	Headers     map[string][]string    `json:"headers"`
	Bytes       int                    `json:"bytes"`
	Lines       int                    `json:"lines"`
	Nums        map[string]int64       `json:"nums"`
	Attachments map[string]*attachment `json:"_attachments"`
	Added       time.Time              `json:"added"`
}

type articleResults struct {
	Rows []struct {
		Key     []interface{} `json:"key"`
		Article article       `json:"doc"`
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

	results := groupResults{}
	err := cb.db.Query("_design/groups/_view/active", map[string]interface{}{
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
		return nil, nntpserver.ErrNoSuchGroup
	}
	return g, nil
}

func (cb *couchBackend) mkArticle(ar article) *nntp.Article {
	url := fmt.Sprintf("%s/%s/article", cb.db.DBURL(), cleanupID(ar.MsgID, true))
	return &nntp.Article{
		Header: textproto.MIMEHeader(ar.Headers),
		Body:   &lazyOpener{url, nil, nil},
		Bytes:  ar.Bytes,
		Lines:  ar.Lines,
	}
}

func (cb *couchBackend) GetArticle(group *nntp.Group, id string) (*nntp.Article, error) {
	var ar article
	if intid, err := strconv.ParseInt(id, 10, 64); err == nil {
		results := articleResults{}
		cb.db.Query("_design/articles/_view/list", map[string]interface{}{
			"include_docs": true,
			"reduce":       false,
			"key":          []interface{}{group.Name, intid},
		}, &results)

		if len(results.Rows) != 1 {
			return nil, nntpserver.ErrInvalidArticleNumber
		}

		ar = results.Rows[0].Article
	} else {
		err := cb.db.Retrieve(cleanupID(id, false), &ar)
		if err != nil {
			return nil, nntpserver.ErrInvalidMessageID
		}
	}

	return cb.mkArticle(ar), nil
}

func (cb *couchBackend) GetArticles(group *nntp.Group,
	from, to int64) ([]nntpserver.NumberedArticle, error) {

	rv := make([]nntpserver.NumberedArticle, 0, 100)

	results := articleResults{}
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

func (cb *couchBackend) AllowPost() bool {
	return true
}

func cleanupID(msgid string, escapedAt bool) string {
	s := strings.TrimFunc(msgid, func(r rune) bool {
		return r == ' ' || r == '<' || r == '>'
	})
	qe := url.QueryEscape(s)
	if escapedAt {
		return qe
	}
	return strings.Replace(qe, "%40", "@", -1)
}

func (cb *couchBackend) Post(art *nntp.Article) error {
	a := article{
		DocType:     "article",
		Headers:     map[string][]string(art.Header),
		Nums:        make(map[string]int64),
		MsgID:       cleanupID(art.Header.Get("Message-Id"), false),
		Attachments: make(map[string]*attachment),
		Added:       time.Now(),
	}

	b := []byte{}
	buf := bytes.NewBuffer(b)
	n, err := io.Copy(buf, art.Body)
	if err != nil {
		return err
	}
	log.Printf("Read %d bytes of body", n)

	b = buf.Bytes()
	a.Bytes = len(b)
	a.Lines = bytes.Count(b, []byte{'\n'})

	a.Attachments["article"] = &attachment{"text/plain", b}

	for _, g := range strings.Split(art.Header.Get("Newsgroups"), ",") {
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
			art.Header["Newsgroups"])
		return nntpserver.ErrPostingFailed
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
			return nntpserver.ErrPostingFailed
		}
	}

	return nil
}

func (cb *couchBackend) Authorized() bool {
	return true
}

func (cb *couchBackend) Authenticate(user, pass string) (nntpserver.Backend, error) {
	return nil, nntpserver.ErrAuthRejected
}

func maybefatal(err error, f string, a ...interface{}) {
	if err != nil {
		log.Fatalf(f, a...)
	}
}

func main() {
	couchURL := flag.String("couch", "http://localhost:5984/news",
		"Couch DB.")

	flag.Parse()

	if *useSyslog {
		sl, err := syslog.New(syslog.LOG_INFO, "nntpd")
		if err != nil {
			log.Fatalf("Error initializing syslog: %v", err)
		}
		log.SetOutput(sl)
		log.SetFlags(0)
	}

	a, err := net.ResolveTCPAddr("tcp", ":1119")
	maybefatal(err, "Error resolving listener: %v", err)
	l, err := net.ListenTCP("tcp", a)
	maybefatal(err, "Error setting up listener: %v", err)
	defer l.Close()

	db, err := couch.Connect(*couchURL)
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
