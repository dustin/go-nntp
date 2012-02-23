package nntpserver

import (
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/dustin/go-nntp"
)

type NNTPError struct {
	Code int
	Msg  string
}

var NoSuchGroup = &NNTPError{411, "No such newsgroup"}

var NoGroupSelected = &NNTPError{412, "No newsgroup selected"}

var InvalidMessageId = &NNTPError{430, "No article with that message-id"}
var InvalidArticleNumber = &NNTPError{423, "No article with that number"}
var NoCurrentArticle = &NNTPError{420, "Current article number is invalid"}

var UnknownCommand = &NNTPError{500, "Unknown command"}
var SyntaxError = &NNTPError{501, "not supported, or syntax error"}

var PostingNotPermitted = &NNTPError{440, "Posting not permitted"}
var PostingFailed = &NNTPError{441, "posting failed"}
var NotWanted = &NNTPError{435, "Article not wanted"}

var AuthRequired = &NNTPError{450, "authorization required"}
var AuthRejected = &NNTPError{452, "authorization rejected"}

// Low-level protocol handler
type Handler func(args []string, s *Server, c *textproto.Conn) error

type NumberedArticle struct {
	Num     int64
	Article *nntp.Article
}

// The backend that provides the things and does the stuff.
type Backend interface {
	ListGroups(max int) ([]*nntp.Group, error)
	GetGroup(name string) (*nntp.Group, error)
	GetArticle(group *nntp.Group, id string) (*nntp.Article, error)
	GetArticles(group *nntp.Group, from, to int64) ([]NumberedArticle, error)
	Authorized() bool
	Authenticate(user, pass string) error
	AllowPost() bool
	Post(article *nntp.Article) error
}

type Server struct {
	Handlers map[string]Handler
	Backend  Backend
	group    *nntp.Group
}

func NewServer(backend Backend) *Server {
	rv := Server{
		Handlers: make(map[string]Handler),
		Backend:  backend,
	}
	rv.Handlers[""] = handleDefault
	rv.Handlers["quit"] = handleQuit
	rv.Handlers["group"] = handleGroup
	rv.Handlers["list"] = handleList
	rv.Handlers["head"] = handleHead
	rv.Handlers["body"] = handleBody
	rv.Handlers["article"] = handleArticle
	rv.Handlers["post"] = handlePost
	rv.Handlers["ihave"] = handleIHave
	rv.Handlers["capabilities"] = handleCap
	rv.Handlers["mode"] = handleMode
	rv.Handlers["authinfo"] = handleAuthInfo
	rv.Handlers["newgroups"] = handleNewGroups
	rv.Handlers["over"] = handleOver
	rv.Handlers["xover"] = handleOver
	return &rv
}

func (e *NNTPError) Error() string {
	return fmt.Sprintf("%d %s", e.Code, e.Msg)
}

func (s *Server) dispatchCommand(cmd string, args []string,
	c *textproto.Conn) (err error) {

	handler, found := s.Handlers[strings.ToLower(cmd)]
	if !found {
		handler, found = s.Handlers[""]
		if !found {
			panic("No default handler.")
		}
	}
	return handler(args, s, c)
}

func (s *Server) Process(tc *net.TCPConn) {
	defer tc.Close()
	c := textproto.NewConn(tc)

	c.PrintfLine("200 Hello!")
	for {
		l, err := c.ReadLine()
		if err != nil {
			log.Printf("Error reading from client, dropping conn: %v", err)
			return
		}
		cmd := strings.Split(l, " ")
		log.Printf("Got cmd:  %+v", cmd)
		args := []string{}
		if len(cmd) > 1 {
			args = cmd[1:]
		}
		err = s.dispatchCommand(cmd[0], args, c)
		if err != nil {
			_, isNNTPError := err.(*NNTPError)
			switch {
			case err == io.EOF:
				// Drop this connection silently. They hung up
				return
			case isNNTPError:
				c.PrintfLine(err.Error())
			default:
				log.Printf("Error dispatching command, dropping conn: %v",
					err)
				return
			}
		}
	}
}

func parseRange(spec string) (low, high int64) {
	if spec == "" {
		return 0, math.MaxInt64
	}
	parts := strings.Split(spec, "-")
	if len(parts) == 1 {
		h, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			h = math.MaxInt64
		}
		return 0, h
	}
	l, _ := strconv.ParseInt(parts[0], 10, 64)
	h, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		h = math.MaxInt64
	}
	return l, h
}

/*
   "0" or article number (see below)
   Subject header content
   From header content
   Date header content
   Message-ID header content
   References header content
   :bytes metadata item
   :lines metadata item
*/

func handleOver(args []string, s *Server, c *textproto.Conn) error {
	if s.group == nil {
		return NoGroupSelected
	}
	from, to := s.group.Low, s.group.High
	articles, err := s.Backend.GetArticles(s.group, from, to)
	if err != nil {
		return err
	}
	c.PrintfLine("224 here it comes")
	dw := c.DotWriter()
	defer dw.Close()
	for _, a := range articles {
		fmt.Fprintf(dw, "%d\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n", a.Num,
			a.Article.Header.Get("Subject"),
			a.Article.Header.Get("From"),
			a.Article.Header.Get("Date"),
			a.Article.Header.Get("Message-Id"),
			a.Article.Header.Get("References"),
			a.Article.Bytes, a.Article.Lines)
	}
	return nil
}

func handleList(args []string, s *Server, c *textproto.Conn) error {
	c.PrintfLine("215 list of newsgroups follows")

	ltype := "active"
	if len(args) > 0 {
		ltype = strings.ToLower(args[0])
	}

	dw := c.DotWriter()
	defer dw.Close()

	if ltype == "overview.fmt" {
		fmt.Fprintln(dw, `Subject:
From:
Date:
Message-ID:
References:
:bytes
:lines`)
	}

	groups, err := s.Backend.ListGroups(-1)
	if err != nil {
		return err
	}
	for _, g := range groups {
		switch ltype {
		case "active":
			fmt.Fprintf(dw, "%s %d %d %v\r\n",
				g.Name, g.High, g.Low, g.Posting)
		case "newsgroups":
			fmt.Fprintf(dw, "%s %s\r\n", g.Name, g.Description)
		}
	}

	return nil
}

func handleNewGroups(args []string, s *Server, c *textproto.Conn) error {
	c.PrintfLine("231 list of newsgroups follows")
	c.PrintfLine(".")
	return nil
}

func handleDefault(args []string, s *Server, c *textproto.Conn) error {
	return UnknownCommand
}

func handleQuit(args []string, s *Server, c *textproto.Conn) error {
	c.PrintfLine("205 bye")
	return io.EOF
}

func handleGroup(args []string, s *Server, c *textproto.Conn) error {
	if len(args) < 1 {
		return NoSuchGroup
	}

	group, err := s.Backend.GetGroup(args[0])
	if err != nil {
		return err
	}

	s.group = group

	c.PrintfLine("211 %d %d %d %s",
		group.Count, group.Low, group.High, group.Name)
	return nil
}

func (s *Server) getArticle(args []string) (*nntp.Article, error) {
	if s.group == nil {
		return nil, NoGroupSelected
	}
	return s.Backend.GetArticle(s.group, args[0])
}

/*
   Syntax
     HEAD message-id
     HEAD number
     HEAD


   First form (message-id specified)
     221 0|n message-id    Headers follow (multi-line)
     430                   No article with that message-id

   Second form (article number specified)
     221 n message-id      Headers follow (multi-line)
     412                   No newsgroup selected
     423                   No article with that number

   Third form (current article number used)
     221 n message-id      Headers follow (multi-line)
     412                   No newsgroup selected
     420                   Current article number is invalid
*/

func handleHead(args []string, s *Server, c *textproto.Conn) error {
	article, err := s.getArticle(args)
	if err != nil {
		return err
	}
	c.PrintfLine("221 1 %s", article.MessageId())
	dw := c.DotWriter()
	defer dw.Close()
	for k, v := range article.Header {
		fmt.Fprintf(dw, "%s: %s\r\n", k, v[0])
	}
	return nil
}

/*
   Syntax
     BODY message-id
     BODY number
     BODY

   Responses

   First form (message-id specified)
     222 0|n message-id    Body follows (multi-line)
     430                   No article with that message-id

   Second form (article number specified)
     222 n message-id      Body follows (multi-line)
     412                   No newsgroup selected
     423                   No article with that number

   Third form (current article number used)
     222 n message-id      Body follows (multi-line)
     412                   No newsgroup selected
     420                   Current article number is invalid

   Parameters
     number        Requested article number
     n             Returned article number
     message-id    Article message-id
*/

func handleBody(args []string, s *Server, c *textproto.Conn) error {
	article, err := s.getArticle(args)
	if err != nil {
		return err
	}
	c.PrintfLine("222 1 %s", article.MessageId())
	dw := c.DotWriter()
	defer dw.Close()
	_, err = io.Copy(dw, article.Body)
	return err
}

/*
   Syntax
     ARTICLE message-id
     ARTICLE number
     ARTICLE

   Responses

   First form (message-id specified)
     220 0|n message-id    Article follows (multi-line)
     430                   No article with that message-id

   Second form (article number specified)
     220 n message-id      Article follows (multi-line)
     412                   No newsgroup selected
     423                   No article with that number

   Third form (current article number used)
     220 n message-id      Article follows (multi-line)
     412                   No newsgroup selected
     420                   Current article number is invalid

   Parameters
     number        Requested article number
     n             Returned article number
     message-id    Article message-id
*/

func handleArticle(args []string, s *Server, c *textproto.Conn) error {
	article, err := s.getArticle(args)
	if err != nil {
		return err
	}
	c.PrintfLine("220 1 %s", article.MessageId())
	dw := c.DotWriter()
	defer dw.Close()

	for k, v := range article.Header {
		fmt.Fprintf(dw, "%s: %s\r\n", k, v[0])
	}

	fmt.Fprintln(dw, "")

	_, err = io.Copy(dw, article.Body)
	return err
}

/*
   Syntax
     POST

   Responses

   Initial responses
     340    Send article to be posted
     440    Posting not permitted

   Subsequent responses
     240    Article received OK
     441    Posting failed
*/

func handlePost(args []string, s *Server, c *textproto.Conn) error {
	if !s.Backend.AllowPost() {
		return PostingNotPermitted
	}

	c.PrintfLine("340 Go ahead")
	var err error
	var article nntp.Article
	article.Header, err = c.ReadMIMEHeader()
	if err != nil {
		return PostingFailed
	}
	article.Body = c.DotReader()
	err = s.Backend.Post(&article)
	if err != nil {
		return err
	}
	c.PrintfLine("240 article received OK")
	return nil
}

func handleIHave(args []string, s *Server, c *textproto.Conn) error {
	if !s.Backend.AllowPost() {
		return NotWanted
	}

	// XXX:  See if we have it.
	article, err := s.Backend.GetArticle(nil, args[0])
	if article != nil {
		return NotWanted
	}

	c.PrintfLine("335 send it")
	article = new(nntp.Article)
	article.Header, err = c.ReadMIMEHeader()
	if err != nil {
		return PostingFailed
	}
	article.Body = c.DotReader()
	err = s.Backend.Post(article)
	if err != nil {
		return err
	}
	c.PrintfLine("235 article received OK")
	return nil
}

func handleCap(args []string, s *Server, c *textproto.Conn) error {
	c.PrintfLine("101 Capability list:")
	dw := c.DotWriter()
	defer dw.Close()

	fmt.Fprintf(dw, "VERSION 2\n")
	fmt.Fprintf(dw, "READER\n")
	if s.Backend.AllowPost() {
		fmt.Fprintf(dw, "POST\n")
		fmt.Fprintf(dw, "IHAVE\n")
	}
	fmt.Fprintf(dw, "OVER\n")
	fmt.Fprintf(dw, "XOVER\n")
	fmt.Fprintf(dw, "LIST ACTIVE NEWSGROUPS OVERVIEW.FMT\n")
	return nil
}

func handleMode(args []string, s *Server, c *textproto.Conn) error {
	if s.Backend.AllowPost() {
		c.PrintfLine("200 Posting allowed")
	} else {
		c.PrintfLine("201 Posting prohibited")
	}
	return nil
}

func handleAuthInfo(args []string, s *Server, c *textproto.Conn) error {
	if len(args) < 2 {
		return SyntaxError
	}
	if strings.ToLower(args[0]) != "user" {
		return SyntaxError
	}

	if s.Backend.Authorized() {
		return c.PrintfLine("250 authenticated")
	}

	c.PrintfLine("350 Continue")
	a, err := c.ReadLine()
	parts := strings.SplitN(a, " ", 3)
	if strings.ToLower(parts[0]) != "authinfo" || strings.ToLower(parts[1]) != "pass" {
		return SyntaxError
	}
	err = s.Backend.Authenticate(args[1], parts[2])
	if err == nil {
		c.PrintfLine("250 authenticated")
	}
	return err
}
