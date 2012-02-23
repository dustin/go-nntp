package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/dustin/go-nntp"
	"github.com/dustin/go-nntp/server"

	"code.google.com/p/dsallings-couch-go"
)

type GroupRow struct {
	Group string        `json:"key"`
	Value []interface{} `json:"value"`
}

type GroupResults struct {
	Rows []GroupRow
}

type Article struct {
	MsgId   string              `json:"_id"`
	DocType string              `json:"type"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
	Nums    map[string]int64    `json:"nums"`
}

type ArticleResults struct {
	Rows []struct {
		Key     []interface{} `json:"key"`
		Article Article       `json:"doc"`
	}
}

type couchBackend struct {
	db *couch.Database
}

func (cb *couchBackend) ListGroups(max int) ([]*nntp.Group, error) {
	results := GroupResults{}
	cb.db.Query("_design/groups/_view/list", map[string]interface{}{
		"group": true,
	}, &results)
	rv := make([]*nntp.Group, 0, 100)
	for _, gr := range results.Rows {
		group := nntp.Group{
			Name:        gr.Group,
			Description: gr.Value[0].(string),
			Count:       int64(gr.Value[1].(float64)),
			Low:         int64(gr.Value[2].(float64)),
			High:        int64(gr.Value[3].(float64)),
		}
		rv = append(rv, &group)
	}
	return rv, nil
}

func (cb *couchBackend) GetGroup(name string) (*nntp.Group, error) {
	results := GroupResults{}
	cb.db.Query("_design/groups/_view/list", map[string]interface{}{
		"group":     true,
		"start_key": name,
		"end_key":   name + "^",
	}, &results)

	if len(results.Rows) < 1 {
		return nil, nntpserver.NoSuchGroup
	} else if len(results.Rows) > 1 {
		log.Printf("Stupid results:  %#v", results.Rows)
	}

	gr := results.Rows[0]
	group := nntp.Group{
		Name:        gr.Group,
		Description: gr.Value[0].(string),
		Count:       int64(gr.Value[1].(float64)),
		Low:         int64(gr.Value[2].(float64)),
		High:        int64(gr.Value[3].(float64)),
	}
	return &group, nil
}

func mkArticle(ar Article) *nntp.Article {
	return &nntp.Article{
		Header: textproto.MIMEHeader(ar.Headers),
		Body:   strings.NewReader(ar.Body),
		Bytes:  len(ar.Body),
		Lines:  strings.Count(ar.Body, "\n"),
	}
}

func (cb *couchBackend) GetArticle(group *nntp.Group, id string) (*nntp.Article, error) {
	var ar Article
	if intid, err := strconv.ParseInt(id, 10, 64); err == nil {
		results := ArticleResults{}
		cb.db.Query("_design/articles/_view/list", map[string]interface{}{
			"include_docs": true,
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

	return mkArticle(ar), nil
}

func (cb *couchBackend) GetArticles(group *nntp.Group,
	from, to int64) ([]nntpserver.NumberedArticle, error) {

	rv := make([]nntpserver.NumberedArticle, 0, 100)

	results := ArticleResults{}
	cb.db.Query("_design/articles/_view/list", map[string]interface{}{
		"include_docs": true,
		"start_key":    []interface{}{group.Name, from},
		"end_key":      []interface{}{group.Name, to},
	}, &results)

	for _, r := range results.Rows {
		rv = append(rv, nntpserver.NumberedArticle{
			Num:     int64(r.Key[1].(float64)),
			Article: mkArticle(r.Article),
		})
	}

	return rv, nil
}

func (tb *couchBackend) AllowPost() bool {
	return true
}

func cleanupId(msgid string) string {
	s1 := strings.TrimFunc(msgid, func(r rune) bool {
		return r == ' ' || r == '<' || r == '>'
	})
	s2 := strings.Replace(s1, "/", "%2f", -1)
	s3 := strings.Replace(s2, "+", "%2b", -1)
	return s3
}

func (cb *couchBackend) Post(article *nntp.Article) error {
	a := Article{
		DocType: "article",
		Headers: map[string][]string(article.Header),
		Nums:    make(map[string]int64),
		MsgId:   cleanupId(article.Header.Get("Message-Id")),
	}

	b := []byte{}
	buf := bytes.NewBuffer(b)
	n, err := io.Copy(buf, article.Body)
	if err != nil {
		return err
	}
	log.Printf("Read %d bytes of body", n)

	a.Body = buf.String()

	for _, g := range strings.Split(article.Header.Get("Newsgroups"), ",") {
		g = strings.TrimSpace(g)
		group, err := cb.GetGroup(g)
		if err == nil {
			a.Nums[g] = group.High + 1
		} else {
			log.Printf("Error getting group %q:  %v", g, err)
		}
	}

	if len(a.Nums) == 0 {
		log.Printf("Found no matching groups in %v",
			article.Header["Newsgroups"])
		return nntpserver.PostingFailed
	}

	_, _, err = cb.db.Insert(&a)
	if err != nil {
		log.Printf("error posting article: %v", err)
		return nntpserver.PostingFailed
	}

	return nil
}

func (tb *couchBackend) Authorized() bool {
	return true
}

func (tb *couchBackend) Authenticate(user, pass string) error {
	return nntpserver.AuthRejected
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
